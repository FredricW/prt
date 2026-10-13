package users

import "github.com/gin-gonic/gin"

// Register adds the user routes.
func Register(g *gin.RouterGroup) {
	g.GET("/:id", get)
}
