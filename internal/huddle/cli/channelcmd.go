// huddle channel: create a channel, or remove one. A join never creates a channel; `channel
// create` is the one act that does.
package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// channelName matches a name a channel may have: the hub's own rule.
var channelName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// channelUsage is the line a malformed channel command answers.
const channelUsage = "channel create <name> | channel rm <name>"

// channel runs the channel words.
func (c *cli) channel(ctx context.Context) int {
	sub, ok := c.shift()
	switch {
	case ok && sub == "create":
		return c.channelCreate(ctx)
	case ok && sub == "rm":
		return c.channelRemove(ctx)
	}
	return c.die(channelUsage, 2)
}

// channelCreate makes a channel on the hub and puts this session in it.
func (c *cli) channelCreate(ctx context.Context) int {
	name, ok := c.shift()
	if !ok || !channelName.MatchString(name) {
		return c.die("channel create <name>: a lowercase letter or digit first, then up to 39 letters, digits and dashes", 2)
	}
	if c.url == "" {
		return c.die(noPort, 5)
	}
	resp, err := HFetch(ctx, c.w, http.MethodPost, c.url+"/api/channels",
		map[string]string{"content-type": "application/json"},
		[]byte(newBody().textAlways("name", name).build().EncodeJSON()), joinCall, "")
	if err != nil {
		return c.die(fmt.Sprintf("Huddle unreachable at %s (start it: huddle up)", c.url), 5)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		answer, _ := jsfmt.DecodeValue(raw)
		if resp.StatusCode == http.StatusConflict {
			return c.die(fmt.Sprintf("channel %s exists; join it with /huddle:join %s", name, name), 2)
		}
		return c.die(errorMessage(answer, resp.StatusCode), exitOf(resp.StatusCode))
	}
	c.say("created channel " + name)
	return c.enterChannel(ctx, name)
}

// channelRemove takes one channel off the hub (the creator only): its history stays on disk.
// Not found exits 4, any other refusal 1.
func (c *cli) channelRemove(ctx context.Context) int {
	name, ok := c.shift()
	if !ok {
		return c.die("channel rm <name>", 2)
	}
	if c.url == "" {
		return c.die(noPort, 5)
	}
	resp, err := HFetch(ctx, c.w, http.MethodDelete, c.url+"/api/channels/"+queryEscape(name),
		map[string]string{"content-type": "application/json"}, nil, 0, "")
	if err != nil {
		return c.die(fmt.Sprintf("Huddle unreachable at %s (start it: huddle up)", c.url), 5)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	value, decodeErr := jsfmt.DecodeValue(raw)
	if decodeErr != nil {
		value = jsfmt.Object(nil, nil)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		code := 1
		if resp.StatusCode == 404 {
			code = 4
		}
		return c.die(errorMessage(value, resp.StatusCode), code)
	}
	c.say(fmt.Sprintf("%s removed; its history is kept at %s", name, textField(value, "kept")))
	return 0
}
