package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"

	"hamster/internal/protocol"
)

type Client struct {
	SocketPath string
}

func (c Client) Call(ctx context.Context, requestID, method string, params any, result any) error {
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", c.SocketPath)
	if err != nil {
		return fmt.Errorf("connect to Hamster daemon: %w", err)
	}
	defer connection.Close()
	payload, err := json.Marshal(params)
	if err != nil {
		return err
	}
	request := protocol.Request{Version: protocol.Version, RequestID: requestID, Method: method, Params: payload}
	if err := protocol.Write(connection, request); err != nil {
		return err
	}
	var response protocol.Response
	if err := protocol.Read(bufio.NewReader(connection), &response); err != nil {
		return err
	}
	if response.Error != nil {
		return fmt.Errorf("%s: %s", response.Error.Code, response.Error.Message)
	}
	if result != nil {
		return json.Unmarshal(response.Result, result)
	}
	return nil
}
