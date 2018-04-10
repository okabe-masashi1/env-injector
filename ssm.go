package main

import (
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials/stscreds"
	"github.com/aws/aws-sdk-go/aws/ec2metadata"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ssm"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// For lazy initialization
var service *ssm.SSM

// Initialize SSM service lazily, and return it.
func getSSMService() *ssm.SSM {
	if service != nil {
		return service
	}

	sess, err := session.NewSessionWithOptions(session.Options{
		SharedConfigState: session.SharedConfigEnable,
	})
	if err != nil {
		log.Fatalf("failed to create a new session.\n %v", err)
	}
	if *sess.Config.Region == "" {
		trace("no explicit region configurations. So now retrieving ec2metadata...")
		region, err := ec2metadata.New(sess).Region()
		if err != nil {
			trace(err)
			log.Fatalf("could not find region configurations")
		}
		sess.Config.Region = aws.String(region)
	}

	if arn := os.Getenv("ENV_INJECTOR_ASSUME_ROLE_ARN"); arn != "" {
		creds := stscreds.NewCredentials(sess, arn)
		service = ssm.New(sess, &aws.Config{Credentials: creds})
	} else {
		service = ssm.New(sess)
	}
	return service
}

func injectEnvironByPath() {
	path := os.Getenv("ENV_INJECTOR_PATH")
	if path == "" {
		trace("no parameter path specified, skipping injection by path")
		return
	}
	tracef("parameter path: %s", path)

	svc := getSSMService()

	var nextToken *string
	for {
		result, err := svc.GetParametersByPath(&ssm.GetParametersByPathInput{
			Path:           aws.String(path),
			WithDecryption: aws.Bool(true),
			NextToken:      nextToken,
		})
		if err != nil {
			log.Fatalf("ssm:GetParametersByPath failed. (path: %s)\n %v", path, err)
		}

		for _, param := range result.Parameters {
			key, err := filepath.Rel(path, aws.StringValue(param.Name))
			if err != nil {
				trace(err)
				continue
			}
			if os.Getenv(key) == "" {
				os.Setenv(key, aws.StringValue(param.Value))
				tracef("env injected: %s", key)
			}
		}
		nextToken = result.NextToken
		if nextToken == nil || aws.StringValue(nextToken) == "" {
			break
		}
	}
}

func injectEnvironByPrefix() {
	prefix := os.Getenv("ENV_INJECTOR_PREFIX")

	if prefix == "" {
		trace("no parameter prefix specified, skipping injection by prefix")
		return
	}
	tracef("parameter prefix: %s", prefix)
	if !strings.HasSuffix(prefix, ".") {
		prefix += "."
	}

	names := []*string{}
	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) == 2 && parts[1] == "" {
			names = append(names, aws.String(prefix+parts[0]))
		}
	}
	if len(names) == 0 {
		trace("nothing to be injected by prefix")
		return
	}

	svc := getSSMService()

	// 'GetParameters' fails entirely when any one of parameters is not permitted to get.
	// So call 'GetParameters' one by one.
	for _, n := range names {
		result, err := svc.GetParameters(&ssm.GetParametersInput{
			Names:          []*string{n},
			WithDecryption: aws.Bool(true),
		})
		if err != nil {
			tracef("failed to get: %s", *n)
			trace(err)
			continue
		}

		for _, key := range result.InvalidParameters {
			tracef("invalid parameter: %s", aws.StringValue(key))
		}
		for _, param := range result.Parameters {
			key := strings.TrimPrefix(aws.StringValue(param.Name), prefix)
			os.Setenv(key, aws.StringValue(param.Value))
			tracef("env injected: %s", key)
		}
	}
}