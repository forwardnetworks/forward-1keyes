package thousandeyes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/forwardnetworks/forward-1keyes/src/ports"
)

var _ ports.ResultsReader = (*Client)(nil)

// NetworkResults reads the network records of a test inside the window, over
// every page. A blocked test's record has no loss figure, only error details,
// so both are kept as they came.
func (c *Client) NetworkResults(ctx context.Context, aid, testID string, window time.Duration) ([]ports.NetworkRecord, error) {
	first := endpoint("/v7/test-results/"+url.PathEscape(testID)+"/network", aid) +
		"&window=" + strconv.FormatInt(int64(window/time.Second), 10) + "s"
	var out []ports.NetworkRecord
	err := c.pages(ctx, first, func(body []byte) error {
		var page struct {
			Results []struct {
				Agent struct {
					ID string `json:"agentId"`
				} `json:"agent"`
				ServerIP     string   `json:"serverIp"`
				Date         string   `json:"date"`
				Loss         *float64 `json:"loss"`
				ErrorDetails string   `json:"errorDetails"`
			} `json:"results"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return fmt.Errorf("thousandeyes adapter: results of test %s: %w", testID, err)
		}
		for _, r := range page.Results {
			at, err := time.Parse(time.RFC3339, r.Date)
			if err != nil {
				return fmt.Errorf("thousandeyes adapter: results of test %s: date: %w", testID, err)
			}
			out = append(out, ports.NetworkRecord{AgentID: r.Agent.ID, ServerIP: r.ServerIP, MeasuredAt: at, Loss: r.Loss, Error: r.ErrorDetails})
		}
		return nil
	})
	return out, err
}
