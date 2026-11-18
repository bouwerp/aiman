package awsiac

import (
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"regexp"
	"strings"
)

// Name limits per kind, the .fifo suffix included.
const (
	MaxBucketName = 63
	MaxTableName  = 255
	MaxQueueName  = 80
	MaxTopicName  = 256
	fifoSuffix    = ".fifo"
	suffixLen     = 12
)

var (
	bucketNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	tableNameRe  = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,255}$`)
	queueNameRe  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)
	topicNameRe  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)
	tfIDCharsRe  = regexp.MustCompile(`[.\[\]"]+`)
	dashesRe     = regexp.MustCompile(`-{2,}`)
)

// NameSuffix is the first 12 characters of the lower-case, unpadded base32
// of sha256("<stack>/<logical id>"): stable across runs and machines.
func NameSuffix(stack, logicalID string) string {
	sum := sha256.Sum256([]byte(stack + "/" + logicalID))
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:])
	return strings.ToLower(enc[:suffixLen])
}

// MaxName is a kind's name limit, .fifo included.
func MaxName(k Kind) int {
	switch k {
	case KindBucket:
		return MaxBucketName
	case KindTable:
		return MaxTableName
	case KindQueue:
		return MaxQueueName
	default:
		return MaxTopicName
	}
}

// GeneratedName is the physical name of a resource whose IaC gives none:
// <stack>-<logical id>-<suffix>, or <prefix><suffix> for a Terraform
// name_prefix or bucket_prefix. A Terraform address has . [ ] and quotes
// replaced by -; a bucket name is lower case with _ as -. The cut to the
// kind's limit comes out of the logical id (or prefix) part, so the suffix
// stays; a FIFO queue or topic ends in .fifo within the limit.
func GeneratedName(k Kind, src Source, stack, logicalID, prefix string, fifo bool) string {
	suffix := NameSuffix(stack, logicalID)
	limit := MaxName(k)
	if fifo {
		limit -= len(fifoSuffix)
	}
	var name string
	if prefix != "" {
		p := cleanName(k, prefix)
		name = cut(p, limit-len(suffix)) + suffix
	} else {
		id := logicalID
		if src == SourceTerraform {
			id = strings.Trim(dashesRe.ReplaceAllString(tfIDCharsRe.ReplaceAllString(id, "-"), "-"), "-")
		}
		id = cleanName(k, id)
		head := cleanName(k, stack) + "-"
		room := limit - len(head) - len(suffix) - 1
		id = strings.Trim(cut(id, room), "-.")
		if id == "" {
			name = head + suffix
		} else {
			name = head + id + "-" + suffix
		}
	}
	if fifo {
		name += fifoSuffix
	}
	return name
}

// cleanName keeps the characters a kind's names allow, turning the rest
// into -.
func cleanName(k Kind, s string) string {
	if k == KindBucket {
		s = strings.ToLower(strings.ReplaceAll(s, "_", "-"))
	}
	var b strings.Builder
	for _, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-'
		if k != KindBucket {
			ok = ok || r >= 'A' && r <= 'Z' || r == '_'
		}
		if k == KindTable || k == KindBucket {
			ok = ok || r == '.'
		}
		if !ok {
			r = '-'
		}
		b.WriteRune(r)
	}
	return b.String()
}

func cut(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) > n {
		return s[:n]
	}
	return s
}

// CheckName says why an explicit name is not usable for a kind, or nil.
func CheckName(k Kind, name string, fifo bool) error {
	base := name
	if k == KindQueue || k == KindTopic {
		var isFifo bool
		base, isFifo = strings.CutSuffix(name, fifoSuffix)
		switch {
		case fifo && !isFifo:
			return fmt.Errorf("name %q of a FIFO %s must end in %s", name, k, fifoSuffix)
		case !fifo && isFifo:
			return fmt.Errorf("name %q ends in %s but the %s is not FIFO", name, fifoSuffix, k)
		}
	}
	var re *regexp.Regexp
	switch k {
	case KindBucket:
		re = bucketNameRe
	case KindTable:
		re = tableNameRe
	case KindQueue:
		re = queueNameRe
	default:
		re = topicNameRe
	}
	if len(name) > MaxName(k) || !re.MatchString(base) {
		return fmt.Errorf("%q is not a valid %s name", name, k)
	}
	if k == KindBucket && (strings.Contains(name, "..") || strings.HasPrefix(name, "xn--")) {
		return fmt.Errorf("%q is not a valid bucket name", name)
	}
	return nil
}

// isFifo reports a FIFO queue or topic.
func (r *Resource) isFifo() bool {
	switch {
	case r.Queue != nil:
		return r.Queue.Fifo
	case r.Topic != nil:
		return r.Topic.Fifo
	}
	return false
}

// name gives every named kind its physical name, skipping a resource whose
// explicit name is unusable or taken by an earlier resource of its kind.
// Subscriptions have no name; their physical name is their ARN, learned.
func (p *Plan) name() error {
	taken := map[string]string{}
	for _, r := range append([]*Resource(nil), p.Resources...) {
		if r.Kind == KindSubscription {
			r.Physical, r.Generated = "", false
			continue
		}
		fifo := r.isFifo()
		r.Generated = r.Name == ""
		if r.Generated {
			r.Physical = GeneratedName(r.Kind, r.Source, r.Stack, r.LogicalID, r.NamePrefix, fifo)
		} else {
			if err := CheckName(r.Kind, r.Name, fifo); err != nil {
				p.skip(r, "name", err.Error())
				continue
			}
			r.Physical = r.Name
		}
		id := string(r.Kind) + "/" + r.Physical
		if r.Kind == KindBucket {
			id = strings.ToLower(id)
		}
		if other, ok := taken[id]; ok {
			p.skip(r, "name", fmt.Sprintf("%s %q is already %s's", r.Kind, r.Physical, other))
			continue
		}
		taken[id] = r.Key()
	}
	return nil
}
