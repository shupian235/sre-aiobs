package awsinspect

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
	"sre-aiobs/internal/config"
	"sre-aiobs/internal/platform"
)

var ErrAuthorization = errors.New("AWS authorization denied")

type alarmClient interface {
	DescribeAlarms(context.Context, *cloudwatch.DescribeAlarmsInput, ...func(*cloudwatch.Options)) (*cloudwatch.DescribeAlarmsOutput, error)
}

type Inspector struct {
	Config aws.Config
}

func (i Inspector) client(ctx context.Context, s config.Source) (alarmClient, error) {
	cfg := i.Config
	cfg.Region = s.Region
	if keys := s.AWSCredentials; keys != nil {
		cfg.Credentials = aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(keys.AccessKeyID, keys.SecretAccessKey, keys.SessionToken))
	}
	if s.AWSAccountID != "" {
		identity, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
		if err != nil {
			return nil, queryError(ctx, "STS identity check", err)
		}
		if aws.ToString(identity.Account) != s.AWSAccountID {
			return nil, errors.New("AWS credentials do not match the configured account")
		}
	}
	return cloudwatch.NewFromConfig(cfg), nil
}

func (i Inspector) Check(ctx context.Context, s config.Source) error {
	client, err := i.client(ctx, s)
	if err != nil {
		return err
	}
	input := alarmInput(s.AlarmPrefix)
	input.MaxRecords = aws.Int32(1)
	_, err = client.DescribeAlarms(ctx, input)
	if err != nil {
		return queryError(ctx, "CloudWatch alarm check", err)
	}
	return nil
}

func (i Inspector) Inspect(ctx context.Context, s config.Source) (platform.Snapshot, []platform.Event, error) {
	client, err := i.client(ctx, s)
	if err != nil {
		return platform.Snapshot{}, nil, err
	}
	return inspect(ctx, client, s.AlarmPrefix)
}

func alarmInput(prefix string) *cloudwatch.DescribeAlarmsInput {
	input := &cloudwatch.DescribeAlarmsInput{MaxRecords: aws.Int32(100)}
	if prefix != "" {
		input.AlarmNamePrefix = aws.String(prefix)
	}
	return input
}

func inspect(ctx context.Context, client alarmClient, prefix string) (platform.Snapshot, []platform.Event, error) {
	var events []platform.Event
	var count int64
	input := alarmInput(prefix)
	seen := map[string]bool{}
	for page := 0; page < 100; page++ {
		data, err := client.DescribeAlarms(ctx, input)
		if err != nil {
			return platform.Snapshot{}, nil, queryError(ctx, "CloudWatch alarm query", err)
		}
		for _, alarm := range data.MetricAlarms {
			count++
			name := strings.Map(func(r rune) rune {
				if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
					return -1
				}
				return r
			}, aws.ToString(alarm.AlarmName))
			switch alarm.StateValue {
			case types.StateValueOk:
			case types.StateValueAlarm:
				events = append(events, platform.Event{Rule: platform.CloudAlarmRule, Target: name, Score: 90, Count: 1})
			case types.StateValueInsufficientData:
				events = append(events, platform.Event{Rule: platform.CloudMissingDataRule, Target: name, Score: 65, Count: 1})
			default:
				return platform.Snapshot{}, nil, errors.New("CloudWatch returned an unknown alarm state")
			}
		}
		cursor := aws.ToString(data.NextToken)
		if cursor == "" {
			return platform.Snapshot{CloudAlarmCount: &count}, events, nil
		}
		if seen[cursor] {
			return platform.Snapshot{}, nil, errors.New("CloudWatch returned a repeated page token")
		}
		seen[cursor] = true
		input.NextToken = data.NextToken
	}
	return platform.Snapshot{}, nil, errors.New("CloudWatch alarm page limit exceeded; narrow the alarm prefix")
}

func queryError(ctx context.Context, operation string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var api smithy.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "AccessDenied", "AccessDeniedException", "UnauthorizedOperation", "UnrecognizedClientException", "InvalidClientTokenId", "ExpiredToken", "ExpiredTokenException":
			return fmt.Errorf("%s: %w", operation, ErrAuthorization)
		}
	}
	return fmt.Errorf("%s failed", operation)
}
