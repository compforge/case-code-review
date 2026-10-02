package viewer

import (
	"encoding/json"

	"github.com/compforge/go-stdx/timeline"
)

func (card *TaskCard) applyTimeline(record map[string]any) error {
	raw, err := json.Marshal(record["update"])
	if err != nil {
		return err
	}
	var update timeline.Update
	if err := json.Unmarshal(raw, &update); err != nil {
		return err
	}
	var current timeline.Document
	if card.Timeline != nil {
		current = *card.Timeline
	}
	document, _, err := timeline.MergeDocument(stringValue(record["timeline_id"]), current, update)
	if err != nil {
		return err
	}
	card.Timeline = &document
	return nil
}
