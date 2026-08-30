package httpgin

import (
	"net/http"
	"github.com/gin-gonic/gin"
)

func NewRouter() http.Handler {
	router := gin.New()

	router.Use(gin.Logger())
	router.Use(gin.Recovery())

	return router
}