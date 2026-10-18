package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

// Public STUN servers: they only tell each side its public address.
var stunServers = []string{"stun:stun.cloudflare.com:3478", "stun:stun.l.google.com:19302"}

// TURNConfig is an optional relay for friends who can't connect directly
// (carrier-grade NAT, strict firewalls). Either a fixed server (e.g. coturn)
// or a Cloudflare Realtime TURN key, for which short-lived logins are minted.
type TURNConfig struct {
	URLs       []string `json:"urls,omitempty"` // e.g. turn:relay.example.com:3478
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`

	CloudflareKeyID    string `json:"cloudflareKeyId,omitempty"`
	CloudflareAPIToken string `json:"cloudflareApiToken,omitempty"`
}

func (t TURNConfig) Configured() bool {
	return len(t.URLs) > 0 || (t.CloudflareKeyID != "" && t.CloudflareAPIToken != "")
}

const cfTTL = 6 * time.Hour

// iceCache keeps minted Cloudflare logins until they're half used up.
type iceCache struct {
	mu      sync.Mutex
	servers []webrtc.ICEServer
	minted  time.Time
	key     string
}

func (c *iceCache) get(t TURNConfig) ([]webrtc.ICEServer, error) {
	ice := []webrtc.ICEServer{{URLs: stunServers}}
	if len(t.URLs) > 0 {
		ice = append(ice, webrtc.ICEServer{URLs: t.URLs, Username: t.Username, Credential: t.Credential})
	}
	if t.CloudflareKeyID == "" || t.CloudflareAPIToken == "" {
		return ice, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.key != t.CloudflareKeyID || time.Since(c.minted) > cfTTL/2 {
		s, err := mintCloudflare(t)
		if err != nil {
			return ice, err
		}
		c.servers, c.minted, c.key = s, time.Now(), t.CloudflareKeyID
	}
	return append(ice, c.servers...), nil
}

// mintCloudflare asks Cloudflare for temporary TURN logins
// (developers.cloudflare.com/realtime/turn/generate-credentials).
func mintCloudflare(t TURNConfig) ([]webrtc.ICEServer, error) {
	body, _ := json.Marshal(map[string]int{"ttl": int(cfTTL.Seconds())})
	req, err := http.NewRequest("POST", "https://rtc.live.cloudflare.com/v1/turn/keys/"+t.CloudflareKeyID+"/credentials/generate-ice-servers", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+t.CloudflareAPIToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("relay login: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("relay login: Cloudflare said %s", resp.Status)
	}
	var out struct {
		ICEServers []struct {
			URLs       []string `json:"urls"`
			Username   string   `json:"username"`
			Credential string   `json:"credential"`
		} `json:"iceServers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("relay login: %w", err)
	}
	var servers []webrtc.ICEServer
	for _, s := range out.ICEServers {
		var urls []string
		for _, u := range s.URLs {
			// Port 53 is blocked by browsers and many networks; STUN we already have.
			if strings.HasPrefix(u, "turn") && !strings.Contains(u, ":53?") && !strings.HasSuffix(u, ":53") {
				urls = append(urls, u)
			}
		}
		if len(urls) > 0 {
			servers = append(servers, webrtc.ICEServer{URLs: urls, Username: s.Username, Credential: s.Credential})
		}
	}
	if len(servers) == 0 {
		return nil, fmt.Errorf("relay login: no TURN servers in Cloudflare's answer")
	}
	return servers, nil
}
