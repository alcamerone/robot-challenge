package robot

type Warehouse interface {
	Robots() []Robot
}

type Robot interface {
	EnqueueTask(commands string) (taskID string, position chan RobotState, err chan error)
	CancelTask(taskID string) error
	CurrentState() RobotState
}

type RobotState struct {
	X        uint
	Y        uint
	HasCrate bool
}

type RobotTaskState string

const (
	RobotTaskStatePending  RobotTaskState = "PENDING"
	RobotTaskStateRunning  RobotTaskState = "RUNNING"
	RobotTaskStateComplete RobotTaskState = "COMPLETE"
	RobotTaskStateFailed   RobotTaskState = "FAILED"
)

type RobotTask struct {
	Id      string
	Command string
	State   RobotTaskState
	Error   string
}
