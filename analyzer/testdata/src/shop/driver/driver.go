package driver

type DB struct{ Conn string }

type Model interface{ TableName() string }

type Option func(*DB)

type Mode int

func (m Mode) String() string { return "mode" }

const ModeFast Mode = 1
