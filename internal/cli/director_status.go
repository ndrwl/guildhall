package cli

// director runs `guildhall director status` (docs/director.md#status).
func (a *App) director(args []string) int {
	return a.usageError("director: not implemented")
}
