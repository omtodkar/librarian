package helix

import (
	"fmt"
	"time"

	helixdb "github.com/HelixDB/helix-go"
)

type Client struct {
	hx   *helixdb.Client
	host string
}

func NewClient(host string) *Client {
	return &Client{
		hx:   helixdb.NewClient(host, helixdb.WithTimeout(30*time.Second)),
		host: host,
	}
}

func (c *Client) Ping() error {
	_, err := c.hx.Query("list_documents")
	if err != nil {
		return fmt.Errorf("failed to connect to HelixDB at %s: %w", c.host, err)
	}
	return nil
}
