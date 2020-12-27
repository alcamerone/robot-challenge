package server_test

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alcamerone/robot-challenge/a-restful/robot"
	"github.com/alcamerone/robot-challenge/a-restful/server"
	"github.com/gocraft/web"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

var testErr = errors.New("an error occurred while processing task")

type FakeRobotTask struct {
	robot.RobotTask
	positions chan robot.RobotState
	errors    chan error
}

type FakeRobot struct {
	lock        sync.Mutex
	queuedTasks []FakeRobotTask
	state       robot.RobotState
	positions   chan robot.RobotState
	stopLatch   chan struct{}
	throwError  bool
}

func (bot *FakeRobot) processTasks() {
	var task FakeRobotTask
	for {
		select {
		case <-bot.stopLatch:
			return
		default:
		}
		bot.lock.Lock()
		if len(bot.queuedTasks) > 0 {
			task = bot.queuedTasks[0]
			bot.queuedTasks = bot.queuedTasks[1:]
			if bot.throwError {
				task.errors <- testErr
				continue
			}
			for _, c := range task.Command {
				switch c {
				case 'N':
					bot.state.Y++
				case 'S':
					bot.state.Y--
				case 'E':
					bot.state.X++
				case 'W':
					bot.state.X--
				default:
					// Ignore spaces
					continue
				}
				// Send on task channel to trigger monitor loop
				task.positions <- bot.state
				// Send on bot channel for test feedback
				bot.positions <- bot.state
			}
		}
		bot.lock.Unlock()
		// Pause the loop for a moment so that new tasks can be enqueued
		<-time.After(100 * time.Millisecond)
	}
}

func NewFakeRobot() *FakeRobot {
	bot := &FakeRobot{
		queuedTasks: make([]FakeRobotTask, 0),
		positions:   make(chan robot.RobotState),
		stopLatch:   make(chan struct{}),
	}
	go bot.processTasks()
	return bot
}

func (bot *FakeRobot) EnqueueTask(commands string) (
	taskId string,
	position chan robot.RobotState,
	err chan error,
) {
	taskId = uuid.New().String()
	positions := make(chan robot.RobotState)
	errors := make(chan error)
	task := FakeRobotTask{
		RobotTask: robot.RobotTask{
			Id:      taskId,
			Command: commands,
		},
		positions: positions,
		errors:    errors,
	}
	bot.lock.Lock()
	defer bot.lock.Unlock()
	bot.queuedTasks = append(bot.queuedTasks, task)
	return taskId, positions, errors
}

func (bot *FakeRobot) CancelTask(taskId string) error {
	if bot.throwError {
		return errors.New("an error occurred while cancelling")
	}
	taskFound := false
	var newTaskList []FakeRobotTask
	bot.lock.Lock()
	defer bot.lock.Unlock()
	for i, task := range bot.queuedTasks {
		if task.Id == taskId {
			if i == len(bot.queuedTasks)-1 {
				newTaskList = bot.queuedTasks[:i]
			} else {
				newTaskList = append(bot.queuedTasks[:i], bot.queuedTasks[i+1:]...)
			}
			taskFound = true
			break
		}
	}
	if !taskFound {
		return fmt.Errorf("task %s not found", taskId)
	}
	bot.queuedTasks = newTaskList
	return nil
}

func (bot *FakeRobot) CurrentState() robot.RobotState {
	return bot.state
}

func (bot *FakeRobot) Stop() {
	close(bot.stopLatch)
}

type TestTaskStatusNotifier struct {
	statesNotified []robot.RobotTask
}

func (ttsn *TestTaskStatusNotifier) NotifyTaskStatus(status robot.RobotTask) {
	ttsn.statesNotified = append(ttsn.statesNotified, status)
}

func getContextInitMiddleware(
	bot *server.Robot,
	notifier server.TaskStatusNotifier,
) func(
	*server.Context,
	web.ResponseWriter,
	*web.Request,
	web.NextMiddlewareFunc,
) {
	return func(
		ctx *server.Context,
		rw web.ResponseWriter,
		req *web.Request,
		next web.NextMiddlewareFunc,
	) {
		ctx.Robot = bot
		ctx.Notifier = notifier
		next(rw, req)
	}
}

func TestMain(m *testing.M) {
	log.SetFlags(log.Lmicroseconds | log.Lshortfile)
	os.Exit(m.Run())
}

func TestPutCommandTaskSuccess(t *testing.T) {
	// N (0,1)
	// E (1,1)
	// E (2,1)
	// N (2,2)
	// N (2,3)
	// W (1,3)
	// S (1,2)
	cmdString := "N E E N N W S"
	bot := NewFakeRobot()
	defer bot.Stop()
	notifier := &TestTaskStatusNotifier{
		statesNotified: make([]robot.RobotTask, 0),
	}
	serveBot := &server.Robot{Robot: bot}
	router := web.New(server.Context{}).
		Middleware(getContextInitMiddleware(serveBot, notifier))
	router = server.AttachRoutes(router)

	rw := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/task", bytes.NewBufferString(cmdString))
	router.ServeHTTP(rw, req)

	require.Equal(t, http.StatusOK, rw.Result().StatusCode)
	var prevState, newState robot.RobotState
	for _, c := range strings.ReplaceAll(cmdString, " ", "") {
		select {
		case newState = <-bot.positions:
			switch c {
			case 'N':
				require.Equal(t, newState.Y, prevState.Y+1)
			case 'S':
				require.Equal(t, newState.Y, prevState.Y-1)
			case 'E':
				require.Equal(t, newState.X, prevState.X+1)
			case 'W':
				require.Equal(t, newState.X, prevState.X-1)
			}
			prevState = newState
		case <-time.After(5 * time.Second):
			t.Fatalf("Did not receive expected position change. Expected next command was %#U", c)
		}
	}
	require.Equal(t, newState.X, uint(1))
	require.Equal(t, newState.Y, uint(2))
	// Wait a moment for the notification to come through
	<-time.After(100 * time.Millisecond)
	require.Len(t, notifier.statesNotified, 1)
	require.Equal(
		t,
		robot.RobotTaskStateComplete,
		notifier.statesNotified[0].State)
}

func TestPutCommandTaskError(t *testing.T) {
	cmdString := "N E E N N W S"
	bot := NewFakeRobot()
	defer bot.Stop()

	// Bot will throw error when trying to process task
	bot.throwError = true

	notifier := &TestTaskStatusNotifier{
		statesNotified: make([]robot.RobotTask, 0),
	}
	serveBot := &server.Robot{Robot: bot}
	router := web.New(server.Context{}).
		Middleware(getContextInitMiddleware(serveBot, notifier))
	router = server.AttachRoutes(router)

	rw := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/task", bytes.NewBufferString(cmdString))
	router.ServeHTTP(rw, req)
	// Request will still succeed as task was accepted
	require.Equal(t, http.StatusOK, rw.Result().StatusCode)

	// Wait a moment for the notification to come through
	<-time.After(500 * time.Millisecond)
	require.Len(t, notifier.statesNotified, 1)
	require.Equal(
		t,
		robot.RobotTaskStateFailed,
		notifier.statesNotified[0].State)
	require.Equal(
		t,
		testErr.Error(),
		notifier.statesNotified[0].Error)
}

// FailReader is a simple io.Reader implementation that allows us
// to test failure behaviour in handlers expecting request bodies
type FailReader struct{}

func (fr *FailReader) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}

func (fr *FailReader) Close() error {
	return nil
}

func TestPutCommandErrorReadingBody(t *testing.T) {
	bot := NewFakeRobot()
	defer bot.Stop()
	notifier := &TestTaskStatusNotifier{
		statesNotified: make([]robot.RobotTask, 0),
	}
	serveBot := &server.Robot{Robot: bot}
	router := web.New(server.Context{}).
		Middleware(getContextInitMiddleware(serveBot, notifier))
	router = server.AttachRoutes(router)

	rw := httptest.NewRecorder()
	// Note use of FailReader to trigger error
	req := httptest.NewRequest("PUT", "/task", &FailReader{})
	router.ServeHTTP(rw, req)
	require.Equal(t, http.StatusInternalServerError, rw.Result().StatusCode)
}

func TestPutCommandInvalidCommandString(t *testing.T) {
	// Note command string contains invalid characters
	cmdString := "N E E 🦀 N W S"
	bot := NewFakeRobot()
	defer bot.Stop()
	notifier := &TestTaskStatusNotifier{
		statesNotified: make([]robot.RobotTask, 0),
	}
	serveBot := &server.Robot{Robot: bot}
	router := web.New(server.Context{}).
		Middleware(getContextInitMiddleware(serveBot, notifier))
	router = server.AttachRoutes(router)

	rw := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/task", bytes.NewBufferString(cmdString))
	router.ServeHTTP(rw, req)
	// Request will still succeed as task was accepted
	require.Equal(t, http.StatusBadRequest, rw.Result().StatusCode)
}

func TestPutCommandUnsafeCommandString(t *testing.T) {
	// Note that this command would cause the robot to drive out of the west wall of the
	// warehouse
	cmdString := "N E E W W W N E E E E E"
	bot := NewFakeRobot()
	defer bot.Stop()

	// Bot will throw error when trying to process task
	bot.throwError = true

	notifier := &TestTaskStatusNotifier{
		statesNotified: make([]robot.RobotTask, 0),
	}
	serveBot := &server.Robot{Robot: bot}
	router := web.New(server.Context{}).
		Middleware(getContextInitMiddleware(serveBot, notifier))
	router = server.AttachRoutes(router)

	rw := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/task", bytes.NewBufferString(cmdString))
	router.ServeHTTP(rw, req)
	require.Equal(t, http.StatusUnprocessableEntity, rw.Result().StatusCode)
}

func TestGetTaskStatus(t *testing.T) {
	cmdString := "N E E N N W S"
	bot := NewFakeRobot()
	// Don't process tasks for this test, as we want to check the task queue
	bot.Stop()
	notifier := &TestTaskStatusNotifier{
		statesNotified: make([]robot.RobotTask, 0),
	}
	serveBot := &server.Robot{Robot: bot}
	router := web.New(server.Context{}).
		Middleware(getContextInitMiddleware(serveBot, notifier))
	router = server.AttachRoutes(router)

	rw := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/task", bytes.NewBufferString(cmdString))
	router.ServeHTTP(rw, req)

	require.Equal(t, http.StatusOK, rw.Result().StatusCode)
	taskId := string(rw.Body.Bytes())

	rw = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/task/"+taskId, nil)
	router.ServeHTTP(rw, req)

	require.Equal(t, http.StatusOK, rw.Result().StatusCode)
	state := robot.RobotTaskState(rw.Body.Bytes())
	require.Equal(t, robot.RobotTaskStatePending, state)
}

func TestGetTaskStatusTaskDoesNotExist(t *testing.T) {
	cmdString := "N E E N N W S"
	bot := NewFakeRobot()
	// Don't process tasks for this test, as we want to check the task queue
	bot.Stop()
	notifier := &TestTaskStatusNotifier{
		statesNotified: make([]robot.RobotTask, 0),
	}
	serveBot := &server.Robot{Robot: bot}
	router := web.New(server.Context{}).
		Middleware(getContextInitMiddleware(serveBot, notifier))
	router = server.AttachRoutes(router)

	rw := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/task", bytes.NewBufferString(cmdString))
	router.ServeHTTP(rw, req)
	require.Equal(t, http.StatusOK, rw.Result().StatusCode)

	rw = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/task/non-existent-task-id", nil)
	router.ServeHTTP(rw, req)
	require.Equal(t, http.StatusNotFound, rw.Result().StatusCode)
}

func TestCancelTask(t *testing.T) {
	cmdStrings := []string{
		"N",
		"N",
		"N",
		"E", // Critical task: if this one is deleted, the next becomes unsafe
		"W",
		"N",
	}
	idxToCancel := 3
	bot := NewFakeRobot()
	// Don't process tasks for this test, as we want to check the task queue
	bot.Stop()
	notifier := &TestTaskStatusNotifier{
		statesNotified: make([]robot.RobotTask, 0),
	}
	serveBot := &server.Robot{Robot: bot}
	router := web.New(server.Context{}).
		Middleware(getContextInitMiddleware(serveBot, notifier))
	router = server.AttachRoutes(router)

	var rw *httptest.ResponseRecorder
	var req *http.Request
	taskIds := make([]string, len(cmdStrings))
	for i, cmdString := range cmdStrings {
		rw = httptest.NewRecorder()
		req = httptest.NewRequest("PUT", "/task", bytes.NewBufferString(cmdString))
		router.ServeHTTP(rw, req)

		require.Equal(t, http.StatusOK, rw.Result().StatusCode)
		require.Len(t, bot.queuedTasks, i+1)
		taskIds[i] = string(rw.Body.Bytes())
	}

	rw = httptest.NewRecorder()
	req = httptest.NewRequest("DELETE", "/task/"+taskIds[idxToCancel], nil)
	router.ServeHTTP(rw, req)

	require.Equal(t, http.StatusOK, rw.Result().StatusCode)
	require.Len(t, bot.queuedTasks, 3)
	// Ensure correct tasks were deleted
	for i, task := range bot.queuedTasks {
		require.Equal(t, taskIds[i], task.Id)
	}
}

func TestCancelTaskNotFound(t *testing.T) {
	cmdString := "N E E N N W S"
	bot := NewFakeRobot()
	// Don't process tasks for this test, as we want to check the task queue
	bot.Stop()
	notifier := &TestTaskStatusNotifier{
		statesNotified: make([]robot.RobotTask, 0),
	}
	serveBot := &server.Robot{Robot: bot}
	router := web.New(server.Context{}).
		Middleware(getContextInitMiddleware(serveBot, notifier))
	router = server.AttachRoutes(router)

	rw := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/task", bytes.NewBufferString(cmdString))
	router.ServeHTTP(rw, req)

	require.Equal(t, http.StatusOK, rw.Result().StatusCode)
	require.Len(t, bot.queuedTasks, 1)

	rw = httptest.NewRecorder()
	req = httptest.NewRequest("DELETE", "/task/non-existent-task-id", nil)
	router.ServeHTTP(rw, req)

	require.Equal(t, http.StatusNotFound, rw.Result().StatusCode)
}

func TestCancelTaskError(t *testing.T) {
	cmdString := "N E E N N W S"
	bot := NewFakeRobot()
	// Don't process tasks for this test, as we want to check the task queue
	bot.Stop()
	bot.throwError = true
	notifier := &TestTaskStatusNotifier{
		statesNotified: make([]robot.RobotTask, 0),
	}
	serveBot := &server.Robot{Robot: bot}
	router := web.New(server.Context{}).
		Middleware(getContextInitMiddleware(serveBot, notifier))
	router = server.AttachRoutes(router)

	rw := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/task", bytes.NewBufferString(cmdString))
	router.ServeHTTP(rw, req)

	require.Equal(t, http.StatusOK, rw.Result().StatusCode)
	require.Len(t, bot.queuedTasks, 1)
	taskId := string(rw.Body.Bytes())

	rw = httptest.NewRecorder()
	req = httptest.NewRequest("DELETE", "/task/"+taskId, nil)
	router.ServeHTTP(rw, req)

	require.Equal(t, http.StatusInternalServerError, rw.Result().StatusCode)
}
