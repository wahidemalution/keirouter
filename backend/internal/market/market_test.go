package market

import (
	"strings"
	"testing"
)

func TestParseSnapshot(t *testing.T) {
	body := `{"ts":1,"models":[{"slug":"ag/x","family":"Antigravity","minAskIn":0.005,"minAskOut":0.025,"maxAskIn":2.5,"maxAskOut":12.5,"lastRate":0.2}]}`
	models, err := ParseSnapshot(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Slug != "ag/x" || models[0].MinAskIn != 0.005 {
		t.Fatalf("unexpected: %+v", models)
	}
}

func TestReadStreamSkipsMalformedFrames(t *testing.T) {
	stream := "data: {\"models\":[{\"slug\":\"a/b\",\"minAskIn\":1,\"minAskOut\":2}]}\n\n" +
		"data: {not json}\n\n" +
		"data: {\"models\":[{\"slug\":\"c/d\",\"minAskIn\":3,\"minAskOut\":4}]}\n\n"
	var batches [][]Model
	err := ReadStream(strings.NewReader(stream), func(m []Model) error { batches = append(batches, m); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 2 || batches[1][0].Slug != "c/d" {
		t.Fatalf("expected 2 good frames, got %+v", batches)
	}
}
