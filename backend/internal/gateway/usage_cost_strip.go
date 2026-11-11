package gateway

import (
	"bytes"
	"encoding/json"
	"strings"
)

// withoutCostFields returns payload with every object key that names a cost,
// a USD amount or a spend removed at any depth. Token-package customers prepay
// in rupiah for a token quota, so USD figures are meaningless to them and
// would disclose internal pricing. A JSON round trip with a key filter keeps
// this independent of how each payload is built, so a cost field added later
// is still hidden.
func withoutCostFields(payload any) (any, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return dropCostKeys(v), nil
}

func dropCostKeys(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if isCostKey(k) {
				delete(x, k)
				continue
			}
			x[k] = dropCostKeys(child)
		}
		return x
	case []any:
		for i, child := range x {
			x[i] = dropCostKeys(child)
		}
		return x
	}
	return v
}

func isCostKey(k string) bool {
	k = strings.ToLower(k)
	// discount (discount_pct on recent requests) is a price relative to list.
	return strings.Contains(k, "cost") || strings.Contains(k, "usd") || strings.Contains(k, "spent") ||
		strings.Contains(k, "discount")
}
