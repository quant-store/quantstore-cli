package driverapi

type CommandSpec struct {
	Use         string
	Short       string
	OperationID string
	Children    []CommandSpec
}
