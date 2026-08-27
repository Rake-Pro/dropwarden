package browser

// launchedChrome is the OS-agnostic handle the session keeps on the Chrome
// process it started. Every platform file builds one of these from
// launchChrome, so session.go never needs a build tag.
type launchedChrome struct {
	pid  int
	kill func() error
}

// Kill terminates the process. It is safe to call more than once; the platform
// kill funcs treat an already-dead process as success.
func (c *launchedChrome) Kill() error {
	if c == nil || c.kill == nil {
		return nil
	}
	return c.kill()
}
