package server

import (
	"context"
	sql "database/sql"
	"github.com/gofrs/uuid"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/console"
	"github.com/jackc/pgtype"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"net/url"
	"sort"
	"time"
)

func (s *ConsoleServer) ListChannelMessages(ctx context.Context, in *console.ListChannelMessagesRequest) (*api.ChannelMessageList, error) {
	const limit = 50

	stream, err := buildStream(in)
	if err != nil {
		return nil, err
	}

	channelId, err := StreamToChannelId(*stream)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	cursor, err := url.QueryUnescape(in.Cursor)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Cursor is invalid or expired.")
	}

	messageList, err := ChannelMessagesList(ctx, s.logger, s.db, uuid.Nil, *stream, channelId, limit, false, cursor)
	if err == runtime.ErrChannelCursorInvalid {
		return nil, status.Error(codes.InvalidArgument, "Cursor is invalid or expired.")
	} else if err != nil {
		return nil, status.Error(codes.Internal, "Error listing messages from channel.")
	}

	return messageList, nil
}

func (s *ConsoleServer) DeleteChannelMessage(ctx context.Context, in *console.DeleteChannelMessageRequest) (*emptypb.Empty, error) {
	messageID, err := uuid.FromString(in.Id)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Requires a valid message ID.")
	}

	query := "DELETE FROM message WHERE id = $1::UUID"
	if _, err := s.db.ExecContext(ctx, query, messageID); err != nil {
		s.logger.Error("Could not delete message.", zap.Error(err))
		return nil, status.Error(codes.Internal, "An error occurred while trying to delete the message.")
	}

	s.logger.Info("Message deleted.", zap.String("message_id", messageID.String()))
	return &emptypb.Empty{}, nil
}

func (s *ConsoleServer) DeleteChannelMessages(ctx context.Context, in *console.DeleteChannelMessagesRequest) (*console.DeleteChannelMessagesResponse, error) {
	query := "DELETE FROM message WHERE create_time < $1::TIMESTAMPTZ"
	deleteBefore := time.Unix(in.Before.Seconds, int64(in.Before.Nanos)).UTC()

	var res sql.Result
	var err error
	if res, err = s.db.ExecContext(ctx, query, &pgtype.Timestamptz{Time: deleteBefore, Status: pgtype.Present}); err != nil {
		s.logger.Error("Could not delete messages.", zap.Error(err))
		return nil, status.Error(codes.Internal, "An error occurred while trying to delete old message.")
	}
	affected, err := res.RowsAffected()
	if err != nil {
		s.logger.Error("Could not count deleted messages.", zap.Error(err))
		return &console.DeleteChannelMessagesResponse{Total: 0}, nil
	}

	s.logger.Info("Messages deleted.", zap.Int64("affected", affected), zap.String("timestamp", deleteBefore.String()))
	return &console.DeleteChannelMessagesResponse{Total: affected}, nil
}

func buildStream(in *console.ListChannelMessagesRequest) (*PresenceStream, error) {
	stream := PresenceStream{}
	var err error
	switch in.Type {
	case console.ListChannelMessagesRequest_ROOM:
		stream.Mode = StreamModeChannel
		if l := len(in.Label); l < 1 || l > 64 {
			return nil, status.Error(codes.InvalidArgument, "Invalid label size.")
		}
		stream.Label = in.Label
	case console.ListChannelMessagesRequest_GROUP:
		stream.Mode = StreamModeGroup
		if stream.Subject, err = uuid.FromString(in.GroupId); err != nil {
			return nil, status.Error(codes.InvalidArgument, "Invalid group ID format.")
		}
	case console.ListChannelMessagesRequest_DIRECT:
		stream.Mode = StreamModeDM
		users := []string{in.UserIdOne, in.UserIdTwo}
		sort.Strings(users)
		if stream.Subject, err = uuid.FromString(users[0]); err != nil {
			return nil, status.Error(codes.InvalidArgument, "Invalid user ID format.")
		}
		if stream.Subcontext, err = uuid.FromString(users[1]); err != nil {
			return nil, status.Error(codes.InvalidArgument, "Invalid user ID format.")
		}
	default:
		return nil, status.Error(codes.Internal, "Invalid chat type.")
	}
	return &stream, nil
}
