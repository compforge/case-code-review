package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/qiankunli/case-code-review/internal/runner/hypothesisreview"
	"github.com/qiankunli/case-code-review/internal/runner/unitreview"
	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/change"
	"github.com/qiankunli/go-stdx/slicesx"
)

const (
	dossierQuietWindow   = 10 * time.Second
	dossierMaxWait       = 30 * time.Second
	dossierMaxHypotheses = 6
	dossierReviewWorkers = 2
)

type dossierCoordinatorConfig struct {
	Context       context.Context
	Units         []unit.Unit
	Changes       []change.Change
	Selections    map[string]fileSelection
	QuietWindow   time.Duration
	MaxWait       time.Duration
	MaxHypotheses int
	Concurrency   int
	ReadPaths     func(string) []string
	Review        func(context.Context, hypothesisreview.Dossier) []hypothesisreview.Assessment
	OnHypothesis  func(unitreview.Hypothesis)
	OnSealed      func(hypothesisreview.Dossier, string)
}

type dossierCoordinator struct {
	config dossierCoordinatorConfig
	units  map[string]unit.Unit

	candidates chan dossierCandidate
	timers     chan dossierTimer
	finish     chan struct{}
	loopDone   chan struct{}

	mu          sync.Mutex
	hypotheses  []unitreview.Hypothesis
	assessments []hypothesisreview.Assessment
	seen        map[string]bool
	sealed      []*sealedDossier
	reviewWG    sync.WaitGroup
	sem         chan struct{}
}

type dossierCandidate struct {
	hypothesis    unitreview.Hypothesis
	unit          unit.Unit
	targetPaths   map[string]bool
	evidencePaths map[string]bool
	readPaths     map[string]bool
	componentRoot string
	directory     string
}

type openDossier struct {
	key        int
	generation int
	candidates []dossierCandidate
	quiet      *time.Timer
	maximum    *time.Timer
}

type sealedDossier struct {
	dossier     hypothesisreview.Dossier
	candidates  []dossierCandidate
	done        chan struct{}
	assessments []hypothesisreview.Assessment
}

type dossierTimer struct {
	key        int
	generation int
	kind       string
}

func newDossierCoordinator(config dossierCoordinatorConfig) *dossierCoordinator {
	if config.Context == nil {
		config.Context = context.Background()
	}
	if config.QuietWindow <= 0 {
		config.QuietWindow = dossierQuietWindow
	}
	if config.MaxWait <= 0 {
		config.MaxWait = dossierMaxWait
	}
	if config.MaxHypotheses <= 0 {
		config.MaxHypotheses = dossierMaxHypotheses
	}
	if config.Concurrency <= 0 {
		config.Concurrency = dossierReviewWorkers
	}
	c := &dossierCoordinator{
		config:     config,
		units:      make(map[string]unit.Unit, len(config.Units)),
		candidates: make(chan dossierCandidate, len(config.Units)*2+1),
		timers:     make(chan dossierTimer, len(config.Units)*4+4),
		finish:     make(chan struct{}), loopDone: make(chan struct{}),
		seen: make(map[string]bool), sem: make(chan struct{}, config.Concurrency),
	}
	for _, reviewUnit := range config.Units {
		c.units[reviewUnit.ID] = reviewUnit
	}
	go c.loop()
	return c
}

func (c *dossierCoordinator) Submit(hypothesis unitreview.Hypothesis) {
	if hypothesis.ID == "" {
		return
	}
	reviewUnit := c.units[hypothesis.OriginUnit]
	candidate := newDossierCandidate(hypothesis, reviewUnit, c.config)
	select {
	case c.candidates <- candidate:
	case <-c.loopDone:
	case <-c.config.Context.Done():
	}
}

func (c *dossierCoordinator) Finish() ([]unitreview.Hypothesis, []hypothesisreview.Assessment) {
	select {
	case c.finish <- struct{}{}:
	case <-c.loopDone:
	}
	<-c.loopDone
	c.reviewWG.Wait()
	c.mu.Lock()
	defer c.mu.Unlock()
	hypotheses := append([]unitreview.Hypothesis(nil), c.hypotheses...)
	assessments := append([]hypothesisreview.Assessment(nil), c.assessments...)
	sort.Slice(hypotheses, func(i, j int) bool { return hypotheses[i].ID < hypotheses[j].ID })
	sort.Slice(assessments, func(i, j int) bool { return assessments[i].HypothesisID < assessments[j].HypothesisID })
	return hypotheses, assessments
}

func (c *dossierCoordinator) loop() {
	defer close(c.loopDone)
	open := make(map[int]*openDossier)
	nextKey := 1
	for {
		select {
		case candidate := <-c.candidates:
			c.addCandidate(open, &nextKey, candidate)

		case timer := <-c.timers:
			current := open[timer.key]
			if current == nil || (timer.kind == "quiet" && timer.generation != current.generation) {
				continue
			}
			c.seal(open, current, timer.kind)

		case <-c.finish:
			// Submit is synchronous but buffered; drain every candidate accepted
			// before Review1Done so a fast finish cannot strand the queue tail.
			for {
				select {
				case candidate := <-c.candidates:
					c.addCandidate(open, &nextKey, candidate)
				default:
					goto drained
				}
			}
		drained:
			for _, current := range open {
				c.seal(open, current, "review1_done")
			}
			return

		case <-c.config.Context.Done():
			for _, current := range open {
				c.seal(open, current, "context_done")
			}
			return
		}
	}
}

func (c *dossierCoordinator) addCandidate(open map[int]*openDossier, nextKey *int, candidate dossierCandidate) {
	c.mu.Lock()
	if c.seen[candidate.hypothesis.ID] {
		c.mu.Unlock()
		return
	}
	c.seen[candidate.hypothesis.ID] = true
	c.hypotheses = append(c.hypotheses, candidate.hypothesis)
	c.mu.Unlock()
	if c.config.OnHypothesis != nil {
		c.config.OnHypothesis(candidate.hypothesis)
	}

	selected := selectOpenDossier(open, candidate)
	if selected == nil {
		selected = &openDossier{key: *nextKey}
		*nextKey = *nextKey + 1
		open[selected.key] = selected
		selected.maximum = c.after(selected, c.config.MaxWait, "maximum")
	}
	selected.candidates = append(selected.candidates, candidate)
	selected.generation++
	if selected.quiet != nil {
		selected.quiet.Stop()
	}
	selected.quiet = c.after(selected, c.config.QuietWindow, "quiet")
	if len(selected.candidates) >= c.config.MaxHypotheses {
		c.seal(open, selected, "capacity")
	}
}

func (c *dossierCoordinator) after(dossier *openDossier, duration time.Duration, kind string) *time.Timer {
	generation := dossier.generation
	return time.AfterFunc(duration, func() {
		select {
		case c.timers <- dossierTimer{key: dossier.key, generation: generation, kind: kind}:
		case <-c.loopDone:
		}
	})
}

func (c *dossierCoordinator) seal(open map[int]*openDossier, current *openDossier, reason string) {
	delete(open, current.key)
	if current.quiet != nil {
		current.quiet.Stop()
	}
	if current.maximum != nil {
		current.maximum.Stop()
	}
	if len(current.candidates) == 0 {
		return
	}
	dossier := buildDossier(current.candidates, c.config.Changes)
	prior := c.relatedSealed(current.candidates)
	for _, related := range prior {
		dossier.PriorDossierIDs = append(dossier.PriorDossierIDs, related.dossier.ID)
	}
	sealed := &sealedDossier{dossier: dossier, candidates: current.candidates, done: make(chan struct{})}
	c.mu.Lock()
	c.sealed = append(c.sealed, sealed)
	c.mu.Unlock()
	if c.config.OnSealed != nil {
		c.config.OnSealed(dossier, reason)
	}
	c.reviewWG.Add(1)
	go c.review(sealed, prior)
}

func (c *dossierCoordinator) review(sealed *sealedDossier, prior []*sealedDossier) {
	defer c.reviewWG.Done()
	defer close(sealed.done)
	// Wait before acquiring the bounded worker slot; otherwise a later Dossier
	// could occupy every slot while its earlier dependency is still queued.
	for _, related := range prior {
		select {
		case <-related.done:
			sealed.dossier.PriorAssessments = append(sealed.dossier.PriorAssessments, related.assessments...)
		case <-c.config.Context.Done():
			return
		}
	}
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-c.config.Context.Done():
		return
	}
	if c.config.Review != nil {
		sealed.assessments = c.config.Review(c.config.Context, sealed.dossier)
	}
	c.mu.Lock()
	c.assessments = append(c.assessments, sealed.assessments...)
	c.mu.Unlock()
}

func (c *dossierCoordinator) relatedSealed(candidates []dossierCandidate) []*sealedDossier {
	c.mu.Lock()
	defer c.mu.Unlock()
	var best *sealedDossier
	bestScore := -1
	for _, sealed := range c.sealed {
		if !candidateGroupsRelated(candidates, sealed.candidates) {
			continue
		}
		score := localityScore(candidates[0], sealed.candidates[0])
		if score > bestScore {
			best, bestScore = sealed, score
		}
	}
	if best == nil {
		return nil
	}
	return []*sealedDossier{best}
}

func newDossierCandidate(h unitreview.Hypothesis, reviewUnit unit.Unit, config dossierCoordinatorConfig) dossierCandidate {
	candidate := dossierCandidate{
		hypothesis: h, unit: reviewUnit,
		targetPaths: make(map[string]bool), evidencePaths: make(map[string]bool), readPaths: make(map[string]bool),
	}
	for _, file := range append(reviewUnit.Paths(), h.Path) {
		if file != "" {
			candidate.targetPaths[file] = true
		}
	}
	for _, evidence := range h.Evidence {
		for _, file := range sourcePaths(evidence) {
			candidate.evidencePaths[file] = true
		}
	}
	if config.ReadPaths != nil {
		for _, file := range config.ReadPaths(h.OriginUnit) {
			if !candidate.targetPaths[file] {
				candidate.readPaths[file] = true
			}
		}
	}
	if selection, ok := config.Selections[h.Path]; ok && selection.HasComponent {
		candidate.componentRoot = selection.Component.Root
		candidate.directory = path.Dir(relativeToComponent(selection.Component.Root, h.Path))
	}
	return candidate
}

func selectOpenDossier(open map[int]*openDossier, candidate dossierCandidate) *openDossier {
	var selected *openDossier
	bestScore := -1
	for _, current := range open {
		if len(current.candidates) == 0 || !candidateRelated(candidate, current.candidates) {
			continue
		}
		score := localityScore(candidate, current.candidates[0])
		if score > bestScore || (score == bestScore && (selected == nil || current.key < selected.key)) {
			selected, bestScore = current, score
		}
	}
	return selected
}

func candidateRelated(candidate dossierCandidate, group []dossierCandidate) bool {
	for _, member := range group {
		if candidate.hypothesis.OriginUnit != "" && candidate.hypothesis.OriginUnit == member.hypothesis.OriginUnit {
			return true
		}
		if intersects(candidate.targetPaths, member.targetPaths) ||
			intersects(candidate.evidencePaths, member.evidencePaths) ||
			intersects(candidate.evidencePaths, member.targetPaths) ||
			intersects(candidate.targetPaths, member.evidencePaths) ||
			intersects(candidate.readPaths, member.targetPaths) ||
			intersects(candidate.targetPaths, member.readPaths) ||
			intersects(candidate.readPaths, member.evidencePaths) ||
			intersects(candidate.evidencePaths, member.readPaths) ||
			intersectsStrings(candidate.unit.AllSymbols(), member.unit.AllSymbols()) ||
			highReadOverlap(candidate.readPaths, member.readPaths) {
			return true
		}
	}
	return false
}

func candidateGroupsRelated(left, right []dossierCandidate) bool {
	for _, candidate := range left {
		if candidateRelated(candidate, right) {
			return true
		}
	}
	return false
}

func highReadOverlap(left, right map[string]bool) bool {
	shared := intersectionSize(left, right)
	if shared < 2 {
		return false
	}
	union := len(left) + len(right) - shared
	return union > 0 && float64(shared)/float64(union) >= 0.5
}

func localityScore(left, right dossierCandidate) int {
	if left.componentRoot == "" || left.componentRoot != right.componentRoot {
		return 0
	}
	return 1 + commonDirectoryDepth(left.directory, right.directory)
}

func commonDirectoryDepth(left, right string) int {
	if left == "." || right == "." {
		return 0
	}
	a, b := strings.Split(left, "/"), strings.Split(right, "/")
	depth := 0
	for depth < len(a) && depth < len(b) && a[depth] == b[depth] {
		depth++
	}
	return depth
}

func buildDossier(candidates []dossierCandidate, changes []change.Change) hypothesisreview.Dossier {
	hypotheses := make([]unitreview.Hypothesis, 0, len(candidates))
	paths := make(map[string]bool)
	var units []unit.Unit
	for _, candidate := range candidates {
		hypotheses = append(hypotheses, candidate.hypothesis)
		units = append(units, candidate.unit)
		for file := range candidate.targetPaths {
			paths[file] = true
		}
		for file := range candidate.evidencePaths {
			paths[file] = true
		}
		for file := range candidate.readPaths {
			paths[file] = true
		}
	}
	sort.Slice(hypotheses, func(i, j int) bool { return hypotheses[i].ID < hypotheses[j].ID })
	ids := make([]string, len(hypotheses))
	for i, hypothesis := range hypotheses {
		ids[i] = hypothesis.ID
	}
	evidencePaths := make([]string, 0, len(paths))
	for file := range paths {
		evidencePaths = append(evidencePaths, file)
	}
	sort.Strings(evidencePaths)
	sum := sha256.Sum256([]byte(strings.Join(ids, "\x00")))
	dossierChanges := make([]change.Change, 0, len(changes))
	for _, changed := range changes {
		if paths[effectivePath(changed)] {
			dossierChanges = append(dossierChanges, changed)
		}
	}
	return hypothesisreview.Dossier{
		ID: "d-" + hex.EncodeToString(sum[:])[:12], Changes: dossierChanges,
		Hypotheses: hypotheses, Clues: collectDossierClues(units), EvidencePaths: evidencePaths,
	}
}

func intersects(left, right map[string]bool) bool { return intersectionSize(left, right) > 0 }

func intersectionSize(left, right map[string]bool) int {
	count := 0
	for value := range left {
		if right[value] {
			count++
		}
	}
	return count
}

func intersectsStrings(left, right []string) bool {
	set := make(map[string]bool, len(left))
	for _, value := range left {
		set[value] = true
	}
	for _, value := range right {
		if set[value] {
			return true
		}
	}
	return false
}

func relativeToComponent(root, file string) string {
	if root == "." || root == "" {
		return file
	}
	return strings.TrimPrefix(strings.TrimPrefix(file, root), "/")
}

var sourcePathPattern = regexp.MustCompile(`[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)*\.[A-Za-z0-9]+`)

func sourcePaths(evidence string) []string {
	return slicesx.Uniq(sourcePathPattern.FindAllString(evidence, -1))
}
