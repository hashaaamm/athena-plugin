package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"{{ cookiecutter.go_module }}/internal/service"
)

// Health is liveness and readiness: unauthenticated, unprefixed — a load balancer does not know
// about /api/v1 — and deliberately two endpoints.
//
// Liveness answers "is this process alive" and touches nothing, so a dependency's outage cannot get
// the container killed. Readiness answers "can this process serve correct answers", which is where
// a dependency check belongs. Cloud Run's startup probe reads the first; a load balancer reads the
// second.
type Health struct {
	Service *service.HealthService
}

// Live reports that the process is up.
//
// @Summary	Live
// @ID		live_health_live_get
// @Tags	health
// @Produce	json
// @Success	200	{object}	map[string]string	"Successful Response"
// @Router	/health/live [get]
func (h Health) Live(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// Ready reports whether every dependency answers, and which build is running.
//
// @Summary	Ready
// @ID		ready_health_ready_get
// @Tags	health
// @Produce	json
// @Success	200	{object}	ReadinessStatus	"Successful Response"
// @Router	/health/ready [get]
func (h Health) Ready(c *gin.Context) {
	readiness := h.Service.Readiness(c.Request.Context())
	status := http.StatusOK
	if !readiness.Ready() {
		// 503, not 500: "try again or send traffic elsewhere", not "this request is broken".
		status = http.StatusServiceUnavailable
	}
{%- if cookiecutter.use_postgres == "yes" %}
	c.JSON(status, ReadinessStatus{
		Status:   readiness.Status,
		Version:  readiness.Version,
		Database: readiness.Database,
	})
{%- else %}
	c.JSON(status, ReadinessStatus{
		Status:  readiness.Status,
		Version: readiness.Version,
	})
{%- endif %}
}
