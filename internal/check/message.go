package check

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/andriotisnikos1/dit/internal/apitypes"
	"github.com/andriotisnikos1/dit/internal/notify"
	"github.com/andriotisnikos1/dit/internal/registry"
	"github.com/andriotisnikos1/dit/internal/store"
)

// BuildMessage renders an event into the transport-independent notification
// payload. Title and body are the only things every channel is guaranteed to
// show, so all the actionable detail goes in the body.
func BuildMessage(watch *store.WatchRecord, event *store.EventRecord) notify.Message {
	msg := notify.Message{
		Title:    Title(watch, event),
		Body:     Body(watch, event),
		Priority: Priority(event.Type),
		Tags:     Tags(event.Type),
	}
	if watch != nil && watch.Registry != "" && watch.Repository != "" {
		msg.URL = "https://" + watch.Registry + "/" + watch.Repository
	}
	return msg
}

// Title is the one-line summary shown as the email subject or ntfy title.
func Title(watch *store.WatchRecord, event *store.EventRecord) string {
	image := imageOf(watch, event)
	switch event.Type {
	case apitypes.EventDigestChanged:
		// imageOf already carries the tag for a tag watch, so only append the
		// ref when it is not already there: "app:v1:v1" reads like a mistake.
		return "digest changed: " + withRef(image, event.Tag)
	case apitypes.EventNewTags:
		return fmt.Sprintf("new tags: %s (%s)", repositoryOf(watch, image), event.Tag)
	case apitypes.EventCheckFailed:
		return "check failed: " + image
	case apitypes.EventCheckRecovered:
		return "check recovered: " + image
	default:
		return fmt.Sprintf("%s: %s", event.Type, image)
	}
}

// withRef appends ":ref" unless the image already ends with it.
func withRef(image, ref string) string {
	if ref == "" || strings.HasSuffix(image, ":"+ref) {
		return image
	}
	return image + ":" + ref
}

// repositoryOf strips any tag or digest suffix from an image reference, so a
// pattern event names the repository rather than one arbitrary tag.
func repositoryOf(watch *store.WatchRecord, image string) string {
	if watch != nil && watch.Registry != "" && watch.Repository != "" {
		return watch.Registry + "/" + watch.Repository
	}
	if i := strings.LastIndex(image, ":"); i > 0 && !strings.Contains(image[i:], "/") {
		return image[:i]
	}
	if i := strings.Index(image, "@"); i > 0 {
		return image[:i]
	}
	return image
}

// Body is the multi-line detail of an event.
func Body(watch *store.WatchRecord, event *store.EventRecord) string {
	var b strings.Builder
	image := imageOf(watch, event)

	writeLine := func(k, v string) {
		if strings.TrimSpace(v) == "" {
			return
		}
		fmt.Fprintf(&b, "%s: %s\n", k, v)
	}

	writeLine("Image", image)
	if watch != nil {
		writeLine("Watch", watch.ID)
		writeLine("Kind", string(watch.Kind))
	}
	writeLine("Event", string(event.Type))
	if event.Tag != "" {
		writeLine("Ref", event.Tag)
	}
	writeLine("Time", event.CreatedAt.UTC().Format(time.RFC3339))

	switch event.Type {
	case apitypes.EventDigestChanged:
		writeLine("Old digest", event.OldDigest)
		writeLine("New digest", event.NewDigest)
	case apitypes.EventNewTags:
		tags := newTagsOf(event)
		fmt.Fprintf(&b, "New tags (%d):\n", len(tags))
		for _, t := range tags {
			if t.Digest != "" {
				fmt.Fprintf(&b, "  - %s (%s)\n", t.Tag, shortDigest(t.Digest))
			} else {
				fmt.Fprintf(&b, "  - %s\n", t.Tag)
			}
		}
	case apitypes.EventCheckFailed, apitypes.EventCheckRecovered:
		if detail := detailString(event, "error"); detail != "" {
			writeLine("Error", detail)
		}
		if detail := detailString(event, "kind"); detail != "" {
			writeLine("Failure kind", detail)
		}
		if watch != nil && watch.ConsecutiveFailures > 0 {
			writeLine("Consecutive failures", fmt.Sprintf("%d", watch.ConsecutiveFailures))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// Priority maps an event type onto an ntfy priority.
func Priority(t apitypes.EventType) string {
	switch t {
	case apitypes.EventDigestChanged, apitypes.EventNewTags:
		return "default"
	case apitypes.EventCheckFailed:
		return "high"
	case apitypes.EventCheckRecovered:
		return "low"
	default:
		return "default"
	}
}

// Tags are the ntfy tags (emoji shortcodes) attached to an event type.
func Tags(t apitypes.EventType) []string {
	switch t {
	case apitypes.EventDigestChanged:
		return []string{"whale", "package"}
	case apitypes.EventNewTags:
		return []string{"package", "sparkles"}
	case apitypes.EventCheckFailed:
		return []string{"warning"}
	case apitypes.EventCheckRecovered:
		return []string{"white_check_mark"}
	default:
		return []string{"bell"}
	}
}

// newTagsOf extracts the new-tag list from an event's detail map. The detail
// map went through JSON, so numbers and nested objects arrive as any.
func newTagsOf(event *store.EventRecord) []apitypes.TagDigest {
	if event.Detail == nil {
		return nil
	}
	raw, ok := event.Detail["new_tags"]
	if !ok {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]apitypes.TagDigest, 0, len(list))
	for _, item := range list {
		switch v := item.(type) {
		case map[string]any:
			tag, _ := v["tag"].(string)
			if tag == "" {
				continue
			}
			digest, _ := v["digest"].(string)
			out = append(out, apitypes.TagDigest{Tag: tag, Digest: digest})
		case string:
			out = append(out, apitypes.TagDigest{Tag: v})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tag < out[j].Tag })
	return out
}

func detailString(event *store.EventRecord, key string) string {
	if event.Detail == nil {
		return ""
	}
	v, ok := event.Detail[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

func imageOf(watch *store.WatchRecord, event *store.EventRecord) string {
	if watch != nil && watch.Image != "" {
		return watch.Image
	}
	if event != nil && event.Image != "" {
		return event.Image
	}
	if watch != nil {
		return watch.Registry + "/" + watch.Repository
	}
	return "unknown image"
}

func shortDigest(d string) string {
	d = strings.TrimPrefix(d, "sha256:")
	if len(d) > 12 {
		return "sha256:" + d[:12]
	}
	return "sha256:" + d
}

// classifyCause maps a registry or internal error onto a failure kind for the
// event detail and the log.
func classifyCause(err error) registry.ErrorKind {
	switch {
	case registry.IsUnauthorized(err):
		return registry.KindUnauthorized
	case registry.IsNotFound(err):
		return registry.KindNotFound
	case registry.IsTransient(err):
		return registry.KindTransient
	default:
		return registry.KindUnknown
	}
}

func notificationStatus(s string) apitypes.NotificationStatus {
	return apitypes.NotificationStatus(s)
}
