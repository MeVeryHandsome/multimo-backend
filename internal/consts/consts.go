package consts

// Version is overridden at build time:
//
//	go build -ldflags "-X multimo-backend/internal/consts.Version=v0.1.0"
var Version = "dev"

const AppName = "multimo"
