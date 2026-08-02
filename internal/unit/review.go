package unit

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// SnapshotKind identifies which side of the reviewed change a file snapshot
// came from. A specific Git ref, when available, remains in FileSnapshot.Ref.
type SnapshotKind string

const (
	CurrentSnapshot  SnapshotKind = "current"
	BaselineSnapshot SnapshotKind = "baseline"
)

// FileSnapshot is immutable file content actually admitted to a review. It is
// kept at full fidelity on the Unit even when an Execution later compacts its
// message projection.
type FileSnapshot struct {
	ID      string       `json:"id"`
	Kind    SnapshotKind `json:"kind"`
	Path    string       `json:"path"`
	Start   int          `json:"start,omitempty"`
	End     int          `json:"end,omitempty"`
	Total   int          `json:"total,omitempty"`
	Ref     string       `json:"ref,omitempty"`
	Content string       `json:"content"`
}

func FileSnapshotIDFor(snapshot FileSnapshot) string {
	return reviewID("fs", string(snapshot.Kind), snapshot.Path,
		strconv.Itoa(snapshot.Start), strconv.Itoa(snapshot.End), strconv.Itoa(snapshot.Total), snapshot.Ref, snapshot.Content)
}

// DiffSnapshot is an additional change slice read while reviewing a Unit. The
// Unit's own target diff remains on Fragments; storing it again here would
// create a second source of truth.
type DiffSnapshot struct {
	ID      string   `json:"id"`
	Paths   []string `json:"paths"`
	Content string   `json:"content"`
}

func DiffSnapshotIDFor(snapshot DiffSnapshot) string {
	paths := append([]string(nil), snapshot.Paths...)
	sort.Strings(paths)
	return reviewID("ds", strings.Join(paths, "\x1f"), snapshot.Content)
}

type SearchKind string

const (
	CodeSearch    SearchKind = "code"
	FileDiscovery SearchKind = "file"
)

// SearchResult retains one repository query and its exact result. Search
// output is re-derivable and therefore projects to a lower-priority,
// independently compactable AgentMessage than file or diff snapshots.
type SearchResult struct {
	ID      string     `json:"id"`
	Kind    SearchKind `json:"kind"`
	Query   string     `json:"query"`
	Paths   []string   `json:"paths,omitempty"`
	Content string     `json:"content"`
}

func SearchResultIDFor(result SearchResult) string {
	paths := append([]string(nil), result.Paths...)
	sort.Strings(paths)
	return reviewID("sr", string(result.Kind), result.Query, strings.Join(paths, "\x1f"), result.Content)
}

func reviewID(prefix string, parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return prefix + "-" + hex.EncodeToString(sum[:])[:12]
}

// Hypothesis is a falsifiable issue claim produced by Review 1. ID identifies
// this Unit-owned occurrence; Fingerprint identifies the same underlying claim
// across sibling Units and revisions for deduplication.
type Hypothesis struct {
	ID                string   `json:"id"`
	Fingerprint       string   `json:"fingerprint"`
	OriginUnit        string   `json:"origin_unit,omitempty"`
	Path              string   `json:"path"`
	Content           string   `json:"content"`
	SuggestionCode    string   `json:"suggestion_code,omitempty"`
	ExistingCode      string   `json:"existing_code,omitempty"`
	StartLine         int      `json:"start_line,omitempty"`
	EndLine           int      `json:"end_line,omitempty"`
	Trigger           string   `json:"trigger,omitempty"`
	Impact            string   `json:"impact,omitempty"`
	ChangeAttribution string   `json:"change_attribution,omitempty"`
	Evidence          []string `json:"evidence,omitempty"`
	Uncertainty       string   `json:"uncertainty,omitempty"`
	Thinking          string   `json:"thinking,omitempty"`
	Alias             string   `json:"alias,omitempty"`
	Category          string   `json:"category,omitempty"`
	Severity          string   `json:"severity,omitempty"`
}

func HypothesisFingerprintFor(h Hypothesis) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		h.Path, h.Content, h.ExistingCode, h.Trigger, h.Impact, h.ChangeAttribution,
	}, "\x00")))
	return "hf-" + hex.EncodeToString(sum[:])[:12]
}

func HypothesisIDFor(h Hypothesis) string {
	fingerprint := h.Fingerprint
	if fingerprint == "" {
		fingerprint = HypothesisFingerprintFor(h)
	}
	sum := sha256.Sum256([]byte(h.OriginUnit + "\x00" + fingerprint))
	return "h-" + hex.EncodeToString(sum[:])[:12]
}

type Support string

const (
	Supported    Support = "supported"
	Contradicted Support = "contradicted"
	Insufficient Support = "insufficient"
)

type Attribution string

const (
	// Caused is factual causation, not intent or blame: reverting the current
	// diff would remove or materially change the hypothesis' trigger or impact.
	Caused             Attribution = "caused"
	PreExisting        Attribution = "pre_existing"
	AttributionUnknown Attribution = "unknown"
)

type DeliveryValue string

const (
	Actionable   DeliveryValue = "actionable"
	LowValue     DeliveryValue = "low_value"
	ValueUnknown DeliveryValue = "unknown"
)

type Novelty string

const (
	Novel            Novelty = "new"
	DuplicateInCase  Novelty = "duplicate_in_case"
	AlreadyDelivered Novelty = "already_delivered"
)

type EvidenceReceipt struct {
	ToolCallID string `json:"tool_call_id,omitempty"`
	Kind       string `json:"kind"`
	Ref        string `json:"ref"`
}

// Assessment is Review 2's current judgment of one Unit-owned Hypothesis.
type Assessment struct {
	HypothesisID     string            `json:"hypothesis_id"`
	Support          Support           `json:"support"`
	Attribution      Attribution       `json:"attribution"`
	Value            DeliveryValue     `json:"value"`
	Novelty          Novelty           `json:"novelty"`
	Reason           string            `json:"reason"`
	Evidence         []string          `json:"evidence,omitempty"`
	EvidenceReceipts []EvidenceReceipt `json:"evidence_receipts,omitempty"`
	ReviewerAlias    string            `json:"reviewer_alias,omitempty"`
	LaneID           string            `json:"lane_id,omitempty"`
	SubmissionIndex  int               `json:"submission_index,omitempty"`
}

type TrialDecision struct {
	HypothesisID string `json:"hypothesis_id"`
	Passed       bool   `json:"passed"`
	Delivered    bool   `json:"delivered"`
}

// ReviewSnapshot is the append-only review state attached to a Unit during one
// run. Execution transcripts remain in Session JSONL; this contains only domain
// facts and accepted stage outputs.
type ReviewSnapshot struct {
	FileSnapshots []FileSnapshot
	RelatedDiffs  []DiffSnapshot
	SearchResults []SearchResult
	Hypotheses    []Hypothesis
	Assessments   []Assessment
	Decisions     []TrialDecision
}

type reviewState struct {
	mu          sync.RWMutex
	files       map[string]FileSnapshot
	diffs       map[string]DiffSnapshot
	searches    map[string]SearchResult
	hypotheses  map[string]Hypothesis
	assessments map[string]Assessment
	decisions   map[string]TrialDecision
}

func newReviewState() *reviewState {
	return &reviewState{
		files: make(map[string]FileSnapshot), diffs: make(map[string]DiffSnapshot),
		searches: make(map[string]SearchResult), hypotheses: make(map[string]Hypothesis),
		assessments: make(map[string]Assessment), decisions: make(map[string]TrialDecision),
	}
}

// InitReviewState gives value-copied Units one shared run-local aggregate.
// Formation calls it eagerly; focused tests and external constructors may call
// it before handing a Unit to concurrent review stages.
func (u *Unit) InitReviewState() {
	if u.review == nil {
		u.review = newReviewState()
	}
}

func (u *Unit) AddFileSnapshot(snapshot FileSnapshot) {
	u.InitReviewState()
	if snapshot.ID == "" {
		snapshot.ID = FileSnapshotIDFor(snapshot)
	}
	u.review.mu.Lock()
	if _, exists := u.review.files[snapshot.ID]; !exists {
		u.review.files[snapshot.ID] = snapshot
	}
	u.review.mu.Unlock()
}

func (u *Unit) AddRelatedDiff(snapshot DiffSnapshot) {
	u.InitReviewState()
	if snapshot.ID == "" {
		snapshot.ID = DiffSnapshotIDFor(snapshot)
	}
	snapshot.Paths = append([]string(nil), snapshot.Paths...)
	u.review.mu.Lock()
	if _, exists := u.review.diffs[snapshot.ID]; !exists {
		u.review.diffs[snapshot.ID] = snapshot
	}
	u.review.mu.Unlock()
}

func (u *Unit) AddSearchResult(result SearchResult) {
	u.InitReviewState()
	if result.ID == "" {
		result.ID = SearchResultIDFor(result)
	}
	result.Paths = append([]string(nil), result.Paths...)
	u.review.mu.Lock()
	if _, exists := u.review.searches[result.ID]; !exists {
		u.review.searches[result.ID] = result
	}
	u.review.mu.Unlock()
}

func (u *Unit) AddHypothesis(hypothesis Hypothesis) {
	u.InitReviewState()
	if hypothesis.Fingerprint == "" {
		hypothesis.Fingerprint = HypothesisFingerprintFor(hypothesis)
	}
	if hypothesis.ID == "" {
		hypothesis.ID = HypothesisIDFor(hypothesis)
	}
	hypothesis.Evidence = append([]string(nil), hypothesis.Evidence...)
	u.review.mu.Lock()
	u.review.hypotheses[hypothesis.ID] = hypothesis
	u.review.mu.Unlock()
}

func (u *Unit) AddAssessment(assessment Assessment) {
	u.InitReviewState()
	assessment.Evidence = append([]string(nil), assessment.Evidence...)
	assessment.EvidenceReceipts = append([]EvidenceReceipt(nil), assessment.EvidenceReceipts...)
	u.review.mu.Lock()
	u.review.assessments[assessment.HypothesisID] = assessment
	u.review.mu.Unlock()
}

func (u *Unit) AddTrialDecision(decision TrialDecision) {
	u.InitReviewState()
	u.review.mu.Lock()
	u.review.decisions[decision.HypothesisID] = decision
	u.review.mu.Unlock()
}

func (u Unit) Review() ReviewSnapshot {
	if u.review == nil {
		return ReviewSnapshot{}
	}
	u.review.mu.RLock()
	defer u.review.mu.RUnlock()
	out := ReviewSnapshot{
		FileSnapshots: make([]FileSnapshot, 0, len(u.review.files)),
		RelatedDiffs:  make([]DiffSnapshot, 0, len(u.review.diffs)),
		SearchResults: make([]SearchResult, 0, len(u.review.searches)),
		Hypotheses:    make([]Hypothesis, 0, len(u.review.hypotheses)),
		Assessments:   make([]Assessment, 0, len(u.review.assessments)),
		Decisions:     make([]TrialDecision, 0, len(u.review.decisions)),
	}
	for _, snapshot := range u.review.files {
		out.FileSnapshots = append(out.FileSnapshots, snapshot)
	}
	for _, snapshot := range u.review.diffs {
		snapshot.Paths = append([]string(nil), snapshot.Paths...)
		out.RelatedDiffs = append(out.RelatedDiffs, snapshot)
	}
	for _, result := range u.review.searches {
		result.Paths = append([]string(nil), result.Paths...)
		out.SearchResults = append(out.SearchResults, result)
	}
	for _, hypothesis := range u.review.hypotheses {
		hypothesis.Evidence = append([]string(nil), hypothesis.Evidence...)
		out.Hypotheses = append(out.Hypotheses, hypothesis)
	}
	for _, assessment := range u.review.assessments {
		assessment.Evidence = append([]string(nil), assessment.Evidence...)
		assessment.EvidenceReceipts = append([]EvidenceReceipt(nil), assessment.EvidenceReceipts...)
		out.Assessments = append(out.Assessments, assessment)
	}
	for _, decision := range u.review.decisions {
		out.Decisions = append(out.Decisions, decision)
	}
	sort.Slice(out.FileSnapshots, func(i, j int) bool { return out.FileSnapshots[i].ID < out.FileSnapshots[j].ID })
	sort.Slice(out.RelatedDiffs, func(i, j int) bool { return out.RelatedDiffs[i].ID < out.RelatedDiffs[j].ID })
	sort.Slice(out.SearchResults, func(i, j int) bool { return out.SearchResults[i].ID < out.SearchResults[j].ID })
	sort.Slice(out.Hypotheses, func(i, j int) bool { return out.Hypotheses[i].ID < out.Hypotheses[j].ID })
	sort.Slice(out.Assessments, func(i, j int) bool { return out.Assessments[i].HypothesisID < out.Assessments[j].HypothesisID })
	sort.Slice(out.Decisions, func(i, j int) bool { return out.Decisions[i].HypothesisID < out.Decisions[j].HypothesisID })
	return out
}
