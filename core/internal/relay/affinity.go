package relay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	affinityTTL        = 5 * time.Minute
	maxAffinityEntries = 16384
	maxPrefixBytes     = 512
)

type affinityEntry struct {
	channelID uint64
	expiresAt time.Time
}

// affinityStore tracks prompt-cache / session -> channel stickiness with Redis backing
// and bounded in-process fallback. All Redis failures fail open.
type affinityStore struct {
	mu        sync.Mutex
	entries   map[string]affinityEntry
	byChannel map[uint64]map[string]struct{}
	redis     *redis.Client
	now       func() time.Time
}

func (s *affinityStore) init(now func() time.Time) {
	if now == nil {
		now = time.Now
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.now == nil {
		s.now = now
	}
	if s.entries == nil {
		s.entries = make(map[string]affinityEntry)
	}
	if s.byChannel == nil {
		s.byChannel = make(map[uint64]map[string]struct{})
	}
}

func (s *affinityStore) useRedis(cli *redis.Client) {
	s.mu.Lock()
	s.redis = cli
	s.mu.Unlock()
}

func (s *affinityStore) timeNow() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// computeAffinityKey derives a bounded cache affinity key from session ID (preferred)
// or the first 512 bytes of canonical prompt text.
func computeAffinityKey(sessionID string, keyID uint64, model string, req map[string]any) string {
	if sid := strings.TrimSpace(sessionID); sid != "" {
		return formatAffinityKey("s", keyID, model, []byte(sid))
	}
	if pck, ok := req["prompt_cache_key"].(string); ok && strings.TrimSpace(pck) != "" {
		return formatAffinityKey("s", keyID, model, []byte(strings.TrimSpace(pck)))
	}
	prefix := extractPromptPrefix(req, maxPrefixBytes)
	if len(prefix) == 0 {
		return ""
	}
	return formatAffinityKey("p", keyID, model, prefix)
}

func formatAffinityKey(kind string, keyID uint64, model string, raw []byte) string {
	sum := sha256.Sum256(raw)
	return kind + ":" + strconv.FormatUint(keyID, 10) + ":" + model + ":" + hex.EncodeToString(sum[:8])
}

func extractPromptPrefix(req map[string]any, limit int) []byte {
	msgs, ok := req["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return nil
	}
	var b strings.Builder
	b.Grow(min(limit, 256))
	for _, rm := range msgs {
		m, ok := rm.(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		if role != "" {
			b.WriteString(role)
			b.WriteByte(':')
		}
		switch content := m["content"].(type) {
		case string:
			b.WriteString(content)
		case []any:
			for _, rawPart := range content {
				part, ok := rawPart.(map[string]any)
				if !ok {
					continue
				}
				if txt, ok := part["text"].(string); ok && txt != "" {
					b.WriteString(txt)
				}
				if b.Len() >= limit {
					break
				}
			}
		}
		if b.Len() >= limit {
			break
		}
		b.WriteByte('\n')
	}
	s := b.String()
	if len(s) == 0 {
		return nil
	}
	if len(s) > limit {
		return []byte(strings.ToValidUTF8(s[:limit], ""))
	}
	return []byte(s)
}

func redisAffKey(affKey string) string {
	return "laiai:aff:" + affKey
}

func redisAffGenKey(channelID uint64) string {
	return "laiai:affgen:" + strconv.FormatUint(channelID, 10)
}

func (s *affinityStore) get(ctx context.Context, affKey string) (uint64, bool) {
	if affKey == "" {
		return 0, false
	}
	s.mu.Lock()
	cli := s.redis
	now := s.timeNow()
	if s.entries != nil {
		if e, ok := s.entries[affKey]; ok {
			if now.Before(e.expiresAt) {
				e.expiresAt = now.Add(affinityTTL)
				s.entries[affKey] = e
				chID := e.channelID
				s.mu.Unlock()
				if cli != nil {
					_ = cli.Expire(ctx, redisAffKey(affKey), affinityTTL).Err()
				}
				return chID, true
			}
			s.deleteLocked(affKey, e.channelID)
		}
	}
	s.mu.Unlock()

	if cli == nil {
		return 0, false
	}
	val, err := cli.Get(ctx, redisAffKey(affKey)).Result()
	if err != nil || val == "" {
		return 0, false
	}
	chStr, genStr, hasGen := strings.Cut(val, ":")
	chID, err := strconv.ParseUint(chStr, 10, 64)
	if err != nil || chID == 0 {
		return 0, false
	}
	if hasGen {
		curGen, gerr := cli.Get(ctx, redisAffGenKey(chID)).Result()
		if gerr == nil && curGen != "" && curGen != genStr {
			_ = cli.Del(ctx, redisAffKey(affKey)).Err()
			return 0, false
		}
	}
	_ = cli.Expire(ctx, redisAffKey(affKey), affinityTTL).Err()
	return chID, true
}

func (s *affinityStore) bind(ctx context.Context, affKey string, channelID uint64) {
	if affKey == "" || channelID == 0 {
		return
	}
	s.init(nil)
	s.mu.Lock()
	now := s.timeNow()
	if old, ok := s.entries[affKey]; ok && old.channelID != channelID {
		s.unlinkChannelLocked(affKey, old.channelID)
	}
	if len(s.entries) >= maxAffinityEntries {
		s.evictSomeLocked(now)
	}
	s.entries[affKey] = affinityEntry{channelID: channelID, expiresAt: now.Add(affinityTTL)}
	if s.byChannel[channelID] == nil {
		s.byChannel[channelID] = make(map[string]struct{})
	}
	s.byChannel[channelID][affKey] = struct{}{}
	cli := s.redis
	s.mu.Unlock()

	if cli != nil {
		gen := "0"
		if g, err := cli.Get(ctx, redisAffGenKey(channelID)).Result(); err == nil && g != "" {
			gen = g
		}
		val := strconv.FormatUint(channelID, 10) + ":" + gen
		_ = cli.Set(ctx, redisAffKey(affKey), val, affinityTTL).Err()
	}
}

func (s *affinityStore) clearLocal() {
	s.mu.Lock()
	s.entries = make(map[string]affinityEntry)
	s.byChannel = make(map[uint64]map[string]struct{})
	s.mu.Unlock()
}

func (s *affinityStore) evictChannel(ctx context.Context, channelID uint64) {
	if channelID == 0 {
		return
	}
	s.mu.Lock()
	if keys := s.byChannel[channelID]; len(keys) > 0 {
		for k := range keys {
			delete(s.entries, k)
		}
		delete(s.byChannel, channelID)
	}
	cli := s.redis
	s.mu.Unlock()

	if cli != nil {
		pipe := cli.Pipeline()
		pipe.Incr(ctx, redisAffGenKey(channelID))
		pipe.Expire(ctx, redisAffGenKey(channelID), time.Hour)
		_, _ = pipe.Exec(ctx)
	}
}

func (s *affinityStore) deleteLocked(affKey string, channelID uint64) {
	delete(s.entries, affKey)
	s.unlinkChannelLocked(affKey, channelID)
}

func (s *affinityStore) unlinkChannelLocked(affKey string, channelID uint64) {
	if m := s.byChannel[channelID]; m != nil {
		delete(m, affKey)
		if len(m) == 0 {
			delete(s.byChannel, channelID)
		}
	}
}

func (s *affinityStore) evictSomeLocked(now time.Time) {
	for k, e := range s.entries {
		if !now.Before(e.expiresAt) {
			s.deleteLocked(k, e.channelID)
		}
	}
	if len(s.entries) < maxAffinityEntries {
		return
	}
	targetDrop := max(1, len(s.entries)/8)
	dropped := 0
	for k, e := range s.entries {
		s.deleteLocked(k, e.channelID)
		dropped++
		if dropped >= targetDrop {
			break
		}
	}
}
