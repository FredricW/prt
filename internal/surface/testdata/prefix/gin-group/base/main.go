package main

import (
	"github.com/gin-gonic/gin"

	"example.com/app/internal/users"
)

func main() {
	r := gin.Default()
	api := r.Group("/api")
	users.Register(api.Group("/users"))
	r.Run()
}
