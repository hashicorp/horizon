package testutils

import (
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/mitchellh/go-testing-interface"
)

func AWSSession(t testing.T) *session.Session {
	return session.New(aws.NewConfig().
		WithEndpoint("http://localhost:4566").
		WithRegion("us-east-1").
		WithCredentials(credentials.NewStaticCredentials("hzn", "hzn", "hzn")).
		WithS3ForcePathStyle(true),
	)
}

func DeleteBucket(api *s3.S3, bucket string) {
	var marker *string

	for {
		objects, err := api.ListObjects(&s3.ListObjectsInput{
			Bucket: aws.String(bucket),
			Marker: marker,
		})

		if err != nil {
			panic(err)
		}

		if len(objects.Contents) == 0 {
			break
		}

		marker = objects.NextMarker

		for _, obj := range objects.Contents {
			_, err = api.DeleteObject(&s3.DeleteObjectInput{
				Bucket: aws.String(bucket),
				Key:    obj.Key,
			})
		}
	}

	api.DeleteBucket(&s3.DeleteBucketInput{
		Bucket: aws.String(bucket),
	})
}
