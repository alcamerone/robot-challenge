package exec

import (
	"log"
	"net/http"

	wrApi "github.com/alcamerone/robot-challenge/a-restful/warehouseRobotApi"
	"github.com/gocraft/web"
)

func main() {
	log.SetFlags(log.Lshortfile | log.Lmicroseconds)
	router := web.New(wrApi.Context{})
	router = wrApi.AttachRoutes(router)
	log.Fatal(http.ListenAndServe(":8080", router))
}
