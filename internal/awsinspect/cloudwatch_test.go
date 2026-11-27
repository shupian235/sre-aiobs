package awsinspect

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/smithy-go"
	"sre-aiobs/internal/platform"
)

type describeFunc func(*cloudwatch.DescribeAlarmsInput) (*cloudwatch.DescribeAlarmsOutput, error)

func (f describeFunc) DescribeAlarms(_ context.Context, input *cloudwatch.DescribeAlarmsInput, _ ...func(*cloudwatch.Options)) (*cloudwatch.DescribeAlarmsOutput, error) {
	return f(input)
}

func TestCloudWatchAlarmPaginationAndStates(t *testing.T) {
	pages := 0
	client := describeFunc(func(input *cloudwatch.DescribeAlarmsInput) (*cloudwatch.DescribeAlarmsOutput, error) {
		pages++
		if aws.ToString(input.AlarmNamePrefix) != "prod-" {
			t.Fatal("configured prefix was not forwarded")
		}
		switch aws.ToString(input.NextToken) {
		case "":
			return &cloudwatch.DescribeAlarmsOutput{NextToken: aws.String("page-two"), MetricAlarms: []types.MetricAlarm{
				{AlarmName: aws.String("prod-healthy"), StateValue: types.StateValueOk},
				{AlarmName: aws.String("prod-cpu\n"), StateValue: types.StateValueAlarm},
			}}, nil
		case "page-two":
			return &cloudwatch.DescribeAlarmsOutput{MetricAlarms: []types.MetricAlarm{
				{AlarmName: aws.String("prod-samples"), StateValue: types.StateValueInsufficientData},
			}}, nil
		default:
			t.Fatal("unexpected cursor")
			return nil, nil
		}
	})
	snapshot, events, err := inspect(context.Background(), client, "prod-")
	if err != nil || pages != 2 || snapshot.CloudAlarmCount == nil || *snapshot.CloudAlarmCount != 3 || len(events) != 2 ||
		events[0].Rule != platform.CloudAlarmRule || events[0].Target != "prod-cpu" || events[1].Rule != platform.CloudMissingDataRule || snapshot.Total != 0 {
		t.Fatalf("unexpected inspector result: %+v %+v %v", snapshot, events, err)
	}
}

func TestCloudWatchPartialFailureDoesNotReportSuccess(t *testing.T) {
	client := describeFunc(func(input *cloudwatch.DescribeAlarmsInput) (*cloudwatch.DescribeAlarmsOutput, error) {
		if input.NextToken == nil {
			return &cloudwatch.DescribeAlarmsOutput{NextToken: aws.String("next")}, nil
		}
		return nil, &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "sensitive upstream details"}
	})
	snapshot, events, err := inspect(context.Background(), client, "")
	if !errors.Is(err, ErrAuthorization) || strings.Contains(err.Error(), "sensitive") || snapshot.CloudAlarmCount != nil || len(events) != 0 {
		t.Fatalf("unexpected partial result: %+v %+v %v", snapshot, events, err)
	}
}
