package harness

import "testing"

func TestLifecycleCases(t *testing.T) {
	for _, c := range LifecycleCases() {
		t.Run(c.Name, func(t *testing.T) {
			res := c.Run(t)
			if res.Err != nil {
				t.Fatal(res.Err)
			}
			for _, m := range res.Metrics {
				if m.Score < 1 {
					t.Errorf("%s: %s", m.Name, m.Reason)
				}
			}
		})
	}
}
