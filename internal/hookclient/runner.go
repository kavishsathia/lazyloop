package hookclient

import (
	"context"
	"fmt"
	"io"

	"hamster/internal/daemon"
	"hamster/internal/hook"
)

func Run(ctx context.Context, client daemon.Client, input io.Reader, output io.Writer) error {
	payload, err := io.ReadAll(io.LimitReader(input, 16<<20))
	if err != nil {
		return err
	}
	event, err := hook.ParseEvent(payload)
	if err != nil {
		return fmt.Errorf("parse Codex hook event: %w", err)
	}
	var decision hook.Decision
	if err := client.Call(ctx, event.RequestID(), "hook.event", hook.HookRequest{Event: event}, &decision); err != nil {
		return err
	}
	result, err := hook.CodexOutput(event.EventName, decision)
	if err != nil {
		return err
	}
	_, err = output.Write(append(result, '\n'))
	return err
}
