package admin

import (
	"os"
	"runtime"

	"github.com/RunicBean/mlogclub-simple/web"
	"github.com/kataras/iris/v12"
)

type CommonController struct {
	Ctx iris.Context
}

func (c *CommonController) GetSystem_info() *web.JsonResult {
	hostname, _ := os.Hostname()
	return web.NewEmptyRspBuilder().
		Put("os", runtime.GOOS).
		Put("arch", runtime.GOARCH).
		Put("numCpu", runtime.NumCPU()).
		Put("goVersion", runtime.Version()).
		Put("hostname", hostname).
		JsonResult()
}
