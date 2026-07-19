package httpserver

import (
	"errors"
	"net/http"

	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/application"
	"github.com/AtulFalle/KubeQueue/apps/control-plane/internal/domain"
	"github.com/gin-gonic/gin"
)

type setupClaimRequest struct {
	InstallationName string `json:"installationName" binding:"required"`
	LocalAdmin       struct {
		Username string `json:"username" binding:"required"`
		Password string `json:"password" binding:"required"`
	} `json:"localAdmin" binding:"required"`
	ProjectName string             `json:"projectName" binding:"required"`
	Namespaces  []string           `json:"namespaces" binding:"required"`
	Policy      domain.SetupPolicy `json:"policy" binding:"required"`
}

func newSetupService(
	repository application.SetupRepository, publicURL string,
) (*application.Setup, error) {
	return application.NewSetup(repository, publicURL)
}

func registerSetupAPI(router *gin.Engine, setup *application.Setup) {
	group := router.Group("/api/v1/setup")
	group.GET("/status", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, setup.Status(c.Request.Context()))
	})
	group.POST("/claim", func(c *gin.Context) {
		var request setupClaimRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			writeError(c, http.StatusBadRequest, "INVALID_SETUP_REQUEST", "valid setup input is required")
			return
		}
		claim, err := setup.Claim(c.Request.Context(), domain.SetupClaimInput{
			InstallationName: request.InstallationName,
			LocalAdmin: domain.SetupLocalAdmin{
				Username: request.LocalAdmin.Username,
				Password: request.LocalAdmin.Password,
			},
			ProjectName: request.ProjectName, Namespaces: request.Namespaces, Policy: request.Policy,
		})
		if err != nil {
			switch {
			case errors.Is(err, domain.ErrSetupUnavailable):
				writeError(c, http.StatusGone, "SETUP_UNAVAILABLE", "first-time setup is unavailable")
			case errors.Is(err, domain.ErrSetupClaimConflict):
				writeError(c, http.StatusConflict, "SETUP_ALREADY_CLAIMED", "setup has already been claimed")
			default:
				writeError(c, http.StatusUnprocessableEntity, "SETUP_CLAIM_REJECTED", err.Error())
			}
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusCreated, claim)
	})
	group.GET("/recovery", func(c *gin.Context) {
		recovery, err := setup.Recovery(c.Request.Context())
		if err != nil {
			writeError(c, http.StatusServiceUnavailable, "RECOVERY_CHECKLIST_UNAVAILABLE",
				"recovery checklist is unavailable")
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, recovery)
	})
}
