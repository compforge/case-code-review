package unit

import "testing"

func TestUnitReviewStateFollowsValueCopiesAcrossStages(t *testing.T) {
	reviewUnit := UnitOf(Fragment{Path: "a.go", Symbols: []string{"a.go::A"}})
	reviewCopy := reviewUnit

	reviewCopy.AddFileSnapshot(FileSnapshot{Kind: CurrentSnapshot, Path: "a.go", Content: "source"})
	hypothesis := Hypothesis{OriginUnit: reviewUnit.ID, Path: "a.go", Content: "issue"}
	hypothesis.Fingerprint = HypothesisFingerprintFor(hypothesis)
	hypothesis.ID = HypothesisIDFor(hypothesis)
	reviewCopy.AddHypothesis(hypothesis)
	reviewCopy.AddAssessment(Assessment{HypothesisID: hypothesis.ID, Support: Supported})
	reviewCopy.AddTrialDecision(TrialDecision{HypothesisID: hypothesis.ID, Passed: true, Delivered: true})

	snapshot := reviewUnit.Review()
	if len(snapshot.FileSnapshots) != 1 || len(snapshot.Hypotheses) != 1 ||
		len(snapshot.Assessments) != 1 || len(snapshot.Decisions) != 1 {
		t.Fatalf("review snapshot = %+v", snapshot)
	}
}

func TestHypothesisIdentitySeparatesOccurrenceFromDedupFingerprint(t *testing.T) {
	left := Hypothesis{OriginUnit: "u-left", Path: "a.go", Content: "same issue"}
	right := Hypothesis{OriginUnit: "u-right", Path: "a.go", Content: "same issue"}
	left.Fingerprint = HypothesisFingerprintFor(left)
	right.Fingerprint = HypothesisFingerprintFor(right)
	left.ID = HypothesisIDFor(left)
	right.ID = HypothesisIDFor(right)

	if left.Fingerprint != right.Fingerprint {
		t.Fatalf("same claim fingerprints differ: %s != %s", left.Fingerprint, right.Fingerprint)
	}
	if left.ID == right.ID {
		t.Fatalf("distinct Unit occurrences share ID %s", left.ID)
	}
}

func TestReviewSnapshotDoesNotExposeStoredSlices(t *testing.T) {
	reviewUnit := Unit{ID: "u"}
	reviewUnit.AddRelatedDiff(DiffSnapshot{Paths: []string{"a.go"}, Content: "diff"})
	reviewUnit.AddSearchResult(SearchResult{Kind: CodeSearch, Query: "Call", Paths: []string{"b.go"}, Content: "hit"})
	reviewUnit.AddHypothesis(Hypothesis{OriginUnit: "u", Path: "a.go", Content: "claim", Evidence: []string{"a.go:1"}})
	hypothesis := reviewUnit.Review().Hypotheses[0]
	reviewUnit.AddAssessment(Assessment{HypothesisID: hypothesis.ID, Evidence: []string{"a.go:1"}, EvidenceReceipts: []EvidenceReceipt{{Kind: "diff", Ref: "a.go"}}})

	snapshot := reviewUnit.Review()
	snapshot.RelatedDiffs[0].Paths[0] = "changed.go"
	snapshot.SearchResults[0].Paths[0] = "changed.go"
	snapshot.Hypotheses[0].Evidence[0] = "changed"
	snapshot.Assessments[0].Evidence[0] = "changed"
	snapshot.Assessments[0].EvidenceReceipts[0].Ref = "changed.go"

	stored := reviewUnit.Review()
	if stored.RelatedDiffs[0].Paths[0] != "a.go" || stored.SearchResults[0].Paths[0] != "b.go" ||
		stored.Hypotheses[0].Evidence[0] != "a.go:1" || stored.Assessments[0].Evidence[0] != "a.go:1" ||
		stored.Assessments[0].EvidenceReceipts[0].Ref != "a.go" {
		t.Fatalf("Review exposed mutable Unit state: %+v", stored)
	}
}
