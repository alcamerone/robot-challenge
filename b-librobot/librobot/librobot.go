package librobot

// Warehouse provides an abstraction of a simulated warehouse containing robots.
type Warehouse interface {
	Robots() []Robot
}

// CrateWarehouse provides an abstraction of a simulated warehouse containing both robots and crates.
type CrateWarehouse interface {
	Warehouse

	AddCrate(x uint, y uint) error
	DelCrate(x uint, y uint) error
}

// Robot provides an abstraction of a warehouse robot which accepts tasks in the form of strings of commands.
type Robot interface {
	EnqueueTask(commands string) (taskID string, position chan RobotState, err chan error)

	CancelTask(taskID string) error

	CurrentState() RobotState
}

// RobotState provides an abstraction of the state of a warehouse robot.
type RobotState struct {
	X        uint
	Y        uint
	HasCrate bool
}

// ALL DONE.
