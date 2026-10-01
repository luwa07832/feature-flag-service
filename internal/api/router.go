package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/luwa07832/feature-flag-service/internal/store"
)

// NewRouter wires the public HTTP surface. Only the health entry is published today; the service
// contract in README.md describes the error shape every entry must keep.
func NewRouter(st *store.Store) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())

	router.GET("/healthz", func(c *gin.Context) {
		if err := st.Ping(); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "storage_unavailable", "message": "database is not available"}})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "database": "ok"})
	})

	router.GET("/api/v1/environments/:environmentID/snapshot", getEnvironmentSnapshot(st))
	router.GET("/api/v1/environments/:environmentID/flags/:flagID/evaluate", getFlagEvaluation(st))
	router.GET("/api/v1/environments/:environmentID/flags/:flagID/changes", getFlagHistory(st))
	router.PUT("/api/v1/environments/:environmentID/flags/:flagID/config", putFlagConfig(st))

	router.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "route_not_found", "message": "no route matches this path"}})
	})
	return router
}
