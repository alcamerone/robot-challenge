# Robot Warehouse

We have installed a robot in our warehouse and now we need to send it commands to control it. We need you to implement the high level RESTful APIs, which can be called from a ground control station.

For convenience the robot moves along a grid in the roof of the warehouse and we have made sure that all of our warehouses are built so that the dimensions of the grid are 10 by 10. We've also made sure that all our warehouses are aligned along north-south and east-west axes. The robot also builds an internal x y coordinate map that aligns to the warehouse's physical dimensions. On the map, point (0, 0) indicates the most south-west and (10, 10) indicates the most north-east.

All of the commands to the robot consist of a single capital letter and different commands are delineated by whitespace.

The robot should accept the following commands:

- N move north
- W move west
- E move east
- S move south

Example command sequences
The command sequence: "N E S W" will move the robot in a full square, returning it to where it started.

If the robot starts in the south-west corner of the warehouse then the following commands will move it to the middle of the warehouse.

"N E N E N E N E"

## Robot SDK Interface 

The robot provids a set of low level SDK functions in GO to control its movement. 

```
type Warehouse interface {
	Robots() []Robot
}

type Robot interface {
	EnqueueTask(commands string) (taskID string, position chan RobotState, err chan error) 

	CancelTask(taskID string) error

	CurrentState() RobotState
}

type RobotState struct {
	X uint
	Y uint
	HasCrate bool
}
```

## Requirements
- Create a RESTful API to accept a series of commands to the robot. 
- Make sure that the robot doesn't try to move outside the warehouse.
- Create a RESTful API to report the command series's execution status.
- Create a RESTful API cancel the command series.
- The RESTful service should be written in Golang.

## Challenge
- The Robot SDK is still under development, you need to find a way to prove your API logic is working.
- The ground control station wants to be notified as soon as the command sequence completed. Please provide a high level design overview how you can achieve it. This overview is not expected to be hugely detailed but should clearly articulate the fundamental concept in your design.

___
## Challenges

> The Robot SDK is still under development, you need to find a way to prove your API logic is working.

I have written a test suite with 98.1% coverage of the server code (the only branches that are untested are where the write to the HTTP connection fails, as this is difficult to mock.) I am confident this at least tests the basic functionality of the server code.

> The ground control station wants to be notified as soon as the command sequence completed. Please provide a high level design overview how you can achieve it. This overview is not expected to be hugely detailed but should clearly articulate the fundamental concept in your design.

I have created a `TaskStatusNotifier` interface, an instance of which is intended to be injected into the request context. The `NotifyTaskStatus` function of this interface is called:
- When a task is popped from the queue and run
- When a task succeeds
- When a task fails

This interface can be implemented with any number of backends for sending messages. For example, an implementation could be written using AWS Simple Notification Service, where status change messages could be written to a channel which is then subscribed to by e.g. a Slack hook, or a serverless Lambda function if code needs to be run in response to the status change.

## Assumptions Made
- Third-party libraries (gocraft/web, google/uuid, stretchr/testify) are acceptable
- No authentication or authorisation of requests is required (e.g. the API is only available on a private subnet), and sending of command strings, execution statuses, and task IDs in cleartext is acceptable
- There is only one node processing requests to this API, and no distributed locking is required

## Notes/Further Work
- I wasn't entirely clear on what the `Warehouse` interface was for, as `Robot`s do not expose IDs or any other way to specify to *which* robot you wish to send a command. So, I have left this unimplemented for now. It would be reasonably straightforward to scale the code up to managing multiple robots in multiple warehouses, e.g. using path parameters to specify the request destination.
- The injection of the clients for communicating with robots and for notifying status changes into the request context is left unimplemented, as the configuration for these sorts of tasks is unspecified. The configuration for communicating with robots could be retrieved from a service-registration/-discovery platform, such as Consul, or even a standard database. The configuration for the notification piece could be retrieved as a static file from S3, or also from a database of some kind (e.g. NoSQL serverless, maybe AWS DynamoDB)
- The brief specifies to "Make sure that the robot doesn't try to move outside the warehouse." This is straightforward when checking new requests. However, the behaviour is undefined when a task is cancelled: commands that were safe at the time of submission may become unsafe if an upstream command is cancelled (see the `TestCancelTask` test). I identified three possible ways to respond to this:
	- Do nothing and make it the caller's responsibility to cancel unsafe tasks
	- Check all upcoming tasks and cancel only the ones that would be rendered unsafe by the cancellation
	- Cancel all downstream tasks of the one that is cancelled and force the caller to resubmit them
- I opted for the latter in this example, as it is the most conservative option, but in a case like this I would want to discuss the decision with my supervisor and/or team before making a decision.