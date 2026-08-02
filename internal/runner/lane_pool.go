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

	"github.com/qiankunli/case-code-review/internal/harness"
	"github.com/qiankunli/case-code-review/internal/runner/hypothesisreview"
	"github.com/qiankunli/case-code-review/internal/runner/unitreview"
	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/change"
	"github.com/qiankunli/go-stdx/slicesx"
)

const laneReviewWorkers = 2

type lanePoolConfig struct {
	Context      context.Context
	Units        []unit.Unit
	Changes      []change.Change
	Selections   map[string]fileSelection
	Concurrency  int
	ReadPaths    func(string) []string
	Review       func(context.Context, hypothesisreview.ReviewInput, *harness.ExecutionResult) hypothesisreview.ReviewResult
	OnHypothesis func(unitreview.Hypothesis)
	OnAssigned   func(hypothesisreview.ReviewInput, string)
}

// lanePool is Review 2's only scheduler. Related Hypotheses share one serial
// Lane and therefore one retained AgentGo context, while unrelated Lanes may
// run in parallel under the global worker bound.
type lanePool struct {
	config lanePoolConfig
	units  map[string]unit.Unit

	candidates chan reviewCandidate
	finish     chan struct{}
	loopDone   chan struct{}

	mu          sync.Mutex
	hypotheses  []unitreview.Hypothesis
	assessments []hypothesisreview.Assessment
	seen        map[string]bool
	laneWG      sync.WaitGroup
	sem         chan struct{}
}

type reviewCandidate struct {
	hypothesis    unitreview.Hypothesis
	unit          unit.Unit
	targetPaths   map[string]bool
	evidencePaths map[string]bool
	readPaths     map[string]bool
	componentRoot string
	directory     string
}

type reviewLane struct {
	id           string
	candidates   []reviewCandidate
	inputs       chan hypothesisreview.ReviewInput
	priorResults []hypothesisreview.Assessment
	evidence     []hypothesisreview.EvidenceReceipt
	continuation *harness.ExecutionResult
}

func newLanePool(config lanePoolConfig) *lanePool {
	if config.Context == nil {
		config.Context = context.Background()
	}
	if config.Concurrency <= 0 {
		config.Concurrency = laneReviewWorkers
	}
	p := &lanePool{
		config:     config,
		units:      make(map[string]unit.Unit, len(config.Units)),
		candidates: make(chan reviewCandidate, len(config.Units)*2+1),
		finish:     make(chan struct{}),
		loopDone:   make(chan struct{}),
		seen:       make(map[string]bool),
		sem:        make(chan struct{}, config.Concurrency),
	}
	for _, reviewUnit := range config.Units {
		p.units[reviewUnit.ID] = reviewUnit
	}
	go p.loop()
	return p
}

func (p *lanePool) Submit(hypothesis unitreview.Hypothesis) {
	if hypothesis.ID == "" {
		return
	}
	candidate := newReviewCandidate(hypothesis, p.units[hypothesis.OriginUnit], p.config)
	select {
	case p.candidates <- candidate:
	case <-p.loopDone:
	case <-p.config.Context.Done():
	}
}

func (p *lanePool) Finish() ([]unitreview.Hypothesis, []hypothesisreview.Assessment) {
	select {
	case p.finish <- struct{}{}:
	case <-p.loopDone:
	}
	<-p.loopDone
	p.laneWG.Wait()
	p.mu.Lock()
	defer p.mu.Unlock()
	hypotheses := append([]unitreview.Hypothesis(nil), p.hypotheses...)
	assessments := append([]hypothesisreview.Assessment(nil), p.assessments...)
	sort.Slice(hypotheses, func(i, j int) bool { return hypotheses[i].ID < hypotheses[j].ID })
	sort.Slice(assessments, func(i, j int) bool { return assessments[i].HypothesisID < assessments[j].HypothesisID })
	return hypotheses, assessments
}

func (p *lanePool) loop() {
	defer close(p.loopDone)
	var lanes []*reviewLane
	for {
		select {
		case candidate := <-p.candidates:
			p.assign(&lanes, candidate)
		case <-p.finish:
			for {
				select {
				case candidate := <-p.candidates:
					p.assign(&lanes, candidate)
				default:
					for _, lane := range lanes {
						close(lane.inputs)
					}
					return
				}
			}
		case <-p.config.Context.Done():
			for _, lane := range lanes {
				close(lane.inputs)
			}
			return
		}
	}
}

func (p *lanePool) assign(lanes *[]*reviewLane, candidate reviewCandidate) {
	p.mu.Lock()
	if p.seen[candidate.hypothesis.ID] {
		p.mu.Unlock()
		return
	}
	p.seen[candidate.hypothesis.ID] = true
	p.hypotheses = append(p.hypotheses, candidate.hypothesis)
	p.mu.Unlock()
	if p.config.OnHypothesis != nil {
		p.config.OnHypothesis(candidate.hypothesis)
	}

	lane := selectLane(*lanes, candidate)
	if lane == nil {
		lane = &reviewLane{
			id:     laneID(candidate.hypothesis.ID),
			inputs: make(chan hypothesisreview.ReviewInput, len(p.units)+1),
		}
		*lanes = append(*lanes, lane)
		p.laneWG.Add(1)
		go p.runLane(lane)
	}
	lane.candidates = append(lane.candidates, candidate)
	input := buildReviewInput(candidate, p.config.Changes)
	input.LaneID = lane.id
	if p.config.OnAssigned != nil {
		p.config.OnAssigned(input, "lane_assigned")
	}
	lane.inputs <- input
}

func (p *lanePool) runLane(lane *reviewLane) {
	defer p.laneWG.Done()
	for input := range lane.inputs {
		input.PriorAssessments = append([]hypothesisreview.Assessment(nil), lane.priorResults...)
		input.PriorEvidence = append([]hypothesisreview.EvidenceReceipt(nil), lane.evidence...)
		select {
		case p.sem <- struct{}{}:
		case <-p.config.Context.Done():
			return
		}
		result := hypothesisreview.ReviewResult{}
		if p.config.Review != nil {
			result = p.config.Review(p.config.Context, input, lane.continuation)
		}
		<-p.sem
		if result.Execution.State != "" {
			execution := result.Execution
			lane.continuation = &execution
		}
		lane.priorResults = append(lane.priorResults, result.Assessments...)
		// The ledger is cumulative. A panicking or failed reviewer may return no
		// result, but must not erase receipts already earned by this Lane.
		if len(result.EvidenceReceipts) > 0 {
			lane.evidence = append([]hypothesisreview.EvidenceReceipt(nil), result.EvidenceReceipts...)
		}
		p.mu.Lock()
		p.assessments = append(p.assessments, result.Assessments...)
		p.mu.Unlock()
	}
}

func laneID(hypothesisID string) string {
	sum := sha256.Sum256([]byte(hypothesisID))
	return "l-" + hex.EncodeToString(sum[:])[:12]
}

func selectLane(lanes []*reviewLane, candidate reviewCandidate) *reviewLane {
	var selected *reviewLane
	bestScore := -1
	for _, lane := range lanes {
		if !candidateRelated(candidate, lane.candidates) {
			continue
		}
		score := 0
		for _, member := range lane.candidates {
			score = max(score, localityScore(candidate, member))
		}
		if score > bestScore || score == bestScore && (selected == nil || lane.id < selected.id) {
			selected, bestScore = lane, score
		}
	}
	return selected
}

func newReviewCandidate(h unitreview.Hypothesis, reviewUnit unit.Unit, config lanePoolConfig) reviewCandidate {
	candidate := reviewCandidate{
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

func candidateRelated(candidate reviewCandidate, group []reviewCandidate) bool {
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

func highReadOverlap(left, right map[string]bool) bool {
	shared := intersectionSize(left, right)
	if shared < 2 {
		return false
	}
	union := len(left) + len(right) - shared
	return union > 0 && float64(shared)/float64(union) >= 0.5
}

func localityScore(left, right reviewCandidate) int {
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

func buildReviewInput(candidate reviewCandidate, changes []change.Change) hypothesisreview.ReviewInput {
	paths := make(map[string]bool)
	for file := range candidate.targetPaths {
		paths[file] = true
	}
	for file := range candidate.evidencePaths {
		paths[file] = true
	}
	for file := range candidate.readPaths {
		paths[file] = true
	}
	evidencePaths := make([]string, 0, len(paths))
	for file := range paths {
		evidencePaths = append(evidencePaths, file)
	}
	sort.Strings(evidencePaths)
	reviewChanges := make([]change.Change, 0, len(changes))
	for _, changed := range changes {
		if paths[effectivePath(changed)] {
			reviewChanges = append(reviewChanges, changed)
		}
	}
	return hypothesisreview.ReviewInput{
		Changes: reviewChanges, Hypothesis: candidate.hypothesis,
		Clues: collectReviewClues([]unit.Unit{candidate.unit}), EvidencePaths: evidencePaths,
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
