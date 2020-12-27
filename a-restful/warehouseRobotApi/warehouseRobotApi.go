package warehouseRobotApi

import (
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/alcamerone/robot-challenge/a-restful/robot"
	"github.com/gocraft/web"
)

// Double-bookkeeping here to make it easier to find tasks by ID
var taskQueue = make([]robot.RobotTask, 0)
var taskQueueLock sync.Mutex

// TODO: some form of garbage collection to stop this map from bloating
var tasks = make(map[string]robot.RobotTask)

// Robot is a wrapper around the robot.Robot interface that stores some useful metadata
type Robot struct {
	robot.Robot
	lock     sync.Mutex
	finalPos robot.RobotState
}

type TaskStatusNotifier interface {
	NotifyTaskStatus(status robot.RobotTask)
}

type Context struct {
	Robot    *Robot
	Notifier TaskStatusNotifier
}

func AttachRoutes(router *web.Router) *web.Router {
	return router.
		Middleware(injectRobot).
		Middleware(injectNotifier).
		Put("/task", handlePutCommand).
		Get("/task/:taskId", handleGetTaskStatus).
		Delete("/task/:taskId", handleCancelTask)
}

func injectRobot(
	ctx *Context,
	rw web.ResponseWriter,
	req *web.Request,
	next web.NextMiddlewareFunc,
) {
	// TODO
	next(rw, req)
}

func injectNotifier(
	ctx *Context,
	rw web.ResponseWriter,
	req *web.Request,
	next web.NextMiddlewareFunc,
) {
	// TODO
	next(rw, req)
}

func isCommandCharacter(c rune) bool {
	return c == 'N' || c == 'S' || c == 'W' || c == 'E' || c == ' '
}

func validateCommandString(cmdString string) error {
	for _, c := range cmdString {
		if !isCommandCharacter(c) {
			log.Printf("invalid command character %#U", c)
			return fmt.Errorf("invalid command character %#U", c)
		}
	}
	return nil
}

func getNewFinalPosition(
	cmdString string,
	bot *Robot,
) (robot.RobotState, error) {
	pos := bot.finalPos
	for _, c := range cmdString {
		switch c {
		case 'N':
			pos.Y++
		case 'S':
			pos.Y--
		case 'E':
			pos.X++
		case 'W':
			pos.X--
		default:
			// Ignore spaces
			continue
		}
		// Position is stored unsigned, so moving past the west or south wall will be
		// represented as an underflow and also caught by the greater-than check
		if pos.X > 10 || pos.Y > 10 {
			return pos, errors.New("unsafe command sequence")
		}
	}
	return pos, nil
}

func monitorTask(
	cmdString string,
	taskId string,
	robotState chan robot.RobotState,
	errors chan error,
	notifier TaskStatusNotifier,
) {
	defer func() {
		taskQueueLock.Lock()
		defer taskQueueLock.Unlock()
		// Remove the completed task from the task queue
		// TODO okay for small task queues, but inefficient for longer ones
		// May need to be optimised in future
		for i, qTask := range taskQueue {
			if qTask.Id == taskId {
				if i == len(taskQueue)-1 {
					// Task is last task in queue - create empty queue
					taskQueue = make([]robot.RobotTask, 0)
				} else {
					taskQueue = append(taskQueue[:i], taskQueue[i+1:]...)
				}
				break
			}
		}
	}()
	cmdString = strings.ReplaceAll(cmdString, " ", "")
	task := tasks[taskId]
	for range cmdString {
		select {
		case <-robotState:
			task.State = robot.RobotTaskStateRunning
			tasks[taskId] = task
		case err := <-errors:
			log.Printf("Received error from robot: %s", err.Error())
			task.Error = err.Error()
			task.State = robot.RobotTaskStateFailed
			notifier.NotifyTaskStatus(task)
			tasks[taskId] = task
			return
		}
	}
	task.State = robot.RobotTaskStateComplete
	notifier.NotifyTaskStatus(task)
	tasks[taskId] = task
}

func handlePutCommand(ctx *Context, rw web.ResponseWriter, req *web.Request) {
	// Retrieve command string from request body
	reqBody, err := ioutil.ReadAll(req.Body)
	if err != nil {
		log.Println("Error reading request body: " + err.Error())
		rw.WriteHeader(http.StatusInternalServerError)
		return
	}
	cmdString := strings.ToUpper(string(reqBody))

	// Ensure the string is a valid command string
	err = validateCommandString(cmdString)
	if err != nil {
		log.Printf("Invalid command string received. String was \"%s\"", cmdString)
		rw.WriteHeader(http.StatusBadRequest)
		return
	}

	// Race conditions become problematic from here on
	ctx.Robot.lock.Lock()
	defer ctx.Robot.lock.Unlock()
	// Ensure commands will not cause robot to leave the warehouse
	finalPos, err := getNewFinalPosition(cmdString, ctx.Robot)
	if err != nil {
		log.Printf(
			"Error: command sequence would cause robot to leave warehouse. Sequence was \"%s\"",
			cmdString)
		rw.WriteHeader(http.StatusUnprocessableEntity)
		return
	}
	ctx.Robot.finalPos = finalPos

	// Enqueue task
	taskId, robotState, errors := ctx.Robot.EnqueueTask(cmdString)

	// Monitor task
	go monitorTask(cmdString, taskId, robotState, errors, ctx.Notifier)
	task := robot.RobotTask{
		Id:      taskId,
		Command: cmdString,
		State:   robot.RobotTaskStatePending,
	}
	tasks[taskId] = task
	taskQueueLock.Lock()
	defer taskQueueLock.Unlock()
	taskQueue = append(taskQueue, task)

	_, err = rw.Write([]byte(taskId))
	if err != nil {
		// Connection is probably broken; not much we can do about this other than log it
		log.Println("Error while writing PUT response: " + err.Error())
	}
}

func handleGetTaskStatus(ctx *Context, rw web.ResponseWriter, req *web.Request) {
	ctx.Robot.lock.Lock()
	defer ctx.Robot.lock.Unlock()
	task := tasks[req.PathParams["taskId"]]
	if task.Id == "" {
		// Task does not exist
		rw.WriteHeader(http.StatusNotFound)
		return
	}
	_, err := rw.Write([]byte(task.State))
	if err != nil {
		// Connection is probably broken; not much we can do about this other than log it
		log.Println("Error while writing GET response: " + err.Error())
	}
}

func handleCancelTask(ctx *Context, rw web.ResponseWriter, req *web.Request) {
	ctx.Robot.lock.Lock()
	defer ctx.Robot.lock.Unlock()
	taskQueueLock.Lock()
	defer taskQueueLock.Unlock()
	taskId := req.PathParams["taskId"]

	// Find task in queue, and all tasks downstream from there
	var tasksToCancel []robot.RobotTask
	for i, task := range taskQueue {
		if task.Id == taskId {
			tasksToCancel = append([]robot.RobotTask(nil), taskQueue[i:]...)
		}
	}
	if len(tasksToCancel) == 0 {
		log.Printf("Error while deleting: task %s not found", taskId)
		rw.WriteHeader(http.StatusNotFound)
		return
	}

	// Cancel task and all tasks downstream
	for _, task := range tasksToCancel {
		// Assumes that the robot will report via channels returned from `EnqueueTask` when
		// a task has been cancelled, and reporting does not need to be done here
		err := ctx.Robot.CancelTask(task.Id)
		if err != nil {
			log.Printf("Error cancelling task %s: %s", taskId, err.Error())
			// Signal that there was an error if cancelling *any* of the tasks fail, as
			// the caller may need to intervene manually
			rw.WriteHeader(http.StatusInternalServerError)
			return
		}
		// Remove the cancelled task from the task queue
		// TODO okay for small task queues, but inefficient for longer ones
		// May need to be optimised in future
		for i, qTask := range taskQueue {
			if qTask.Id == task.Id {
				if i == len(taskQueue)-1 {
					// Task is last task in queue
					taskQueue = taskQueue[i:]
				} else {
					taskQueue = append(taskQueue[:i], taskQueue[i+1:]...)
				}
				break
			}
		}
	}
	rw.WriteHeader(http.StatusOK)
}
