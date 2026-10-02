package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/luwa07832/feature-flag-service/internal/store"
)

// NewRouter wires the public HTTP surface. Every entry keeps the error shape
// described in README.md: a single top-level error object with code/message.
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

	v1 := router.Group("/api/v1")
	{
		v1.POST("/environments", createEnvironment(st))
		v1.POST("/flags", createFlagDef(st))
		v1.GET("/flags", listFlags(st))
		v1.GET("/flags/:flagKey", getFlagDef(st))
		v1.PUT("/flags/:flagKey/definition", putFlagDefinition(st))
		v1.GET("/flag-definitions", searchFlagDefinitions(st))
		v1.GET("/compare/:flagKey", compareFlags(st))
		v1.GET("/environments/:environment/evaluate", evaluate(st))
		v1.GET("/environments/:environment/evaluate-at", evaluateAt(st))
		v1.GET("/environments/:environment/changes", getChanges(st))
		v1.GET("/environments/:environment/flags/:flagKey/explain", explainFlag(st))
		v1.GET("/environments/:environment/flags/:flagKey/history", getHistory(st))
		v1.PUT("/environments/:environment/flags/:flagKey/config", putConfig(st))
		v1.DELETE("/environments/:environment/flags/:flagKey/config", deleteConfig(st))
	}

	router.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "route_not_found", "message": "no route matches this path"}})
	})
	return router
}
