package health

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func Live(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status": "ok",
	})
}

func Ready(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")

		if db == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"status": "not_ready",
			})
			return
		}

		pool, err := db.DB()
		if err != nil || pool == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"status": "not_ready",
			})
			return
		}

		ctx, cancel := context.WithTimeout(
			c.Request.Context(),
			time.Second,
		)
		defer cancel()

		if err := pool.PingContext(ctx); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"status": "not_ready",
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"status": "ok",
		})
	}
}
