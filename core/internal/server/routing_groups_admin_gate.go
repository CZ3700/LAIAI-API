package server

import (
	"context"
	"crypto/rand"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
)

type routingAdminReadGate struct {
	mu     sync.Mutex
	active int
	actors map[uint64]int
}

func (g *routingAdminReadGate) acquire(actor uint64) (func(), bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if actor == 0 || g.active >= 4 || g.actors[actor] >= 2 {
		return nil, false
	}
	if g.actors == nil {
		g.actors = make(map[uint64]int)
	}
	g.active++
	g.actors[actor]++
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.active--
			g.actors[actor]--
			if g.actors[actor] == 0 {
				delete(g.actors, actor)
			}
		})
	}, true
}

func routingAdminActor(c *gin.Context) uint64 {
	role, _ := c.Get("actor_role")
	value, _ := c.Get("actor_id")
	actor, _ := value.(uint64)
	if role != identity.PlatRoleSuperadmin {
		return 0
	}
	return actor
}

// Existing admin middleware has checked the active user in the primary DB.
// A shared root token has actor 0, including when both credentials are sent.
func requireNamedRoutingAdmin(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	if routingAdminActor(c) == 0 {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "named superadmin session required", "code": "named_superadmin_required"})
		return
	}
	requestID := rand.Text()
	c.Set("routing_admin_request_id", requestID)
	c.Header("X-Request-Id", requestID)
	c.Next()
}

func (s *Server) routingAdminRead(c *gin.Context, fn func(context.Context) error) error {
	release, ok := s.routingGroupReads.acquire(routingAdminActor(c))
	if !ok {
		return errRoutingAdminBusy
	}
	defer release()
	ctx, cancel := context.WithTimeout(c.Request.Context(), time.Second)
	defer cancel()
	err := fn(ctx)
	if err == nil {
		err = ctx.Err()
	}
	return err
}
