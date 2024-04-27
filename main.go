package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	"github.com/aws/aws-sdk-go-v2/service/apigateway/types"
)

var (
	DefaultRegions = []string{
		"us-east-1", "us-east-2",
	}
)

type ApiGateway struct {
	Site      string
	Name      string
	Endpoints []string
	Regions   []string
}

func randomIpv4() net.IP {
	buf := make([]byte, 4)
	ip := rand.Uint32()
	binary.LittleEndian.PutUint32(buf, ip)
	return buf
}

func NewApiGateway(site, name string) (*ApiGateway, error) {
	if site[len(site)-1] == '/' {
		site = strings.TrimRight(site, "/")
	}

	return &ApiGateway{
		Site:      site,
		Name:      name,
		Endpoints: []string{},
		Regions:   DefaultRegions,
	}, nil
}

// ApiExistsInRegion check if an api already exists in region
func ApiExistsInRegion(client *apigateway.Client, name string, region string) bool {
	output, err := client.GetRestApis(context.TODO(), &apigateway.GetRestApisInput{})
	if err != nil {
		panic(err)
	}

	for _, api := range output.Items {
		if *api.Name == name {
			return true
		}
	}
	return false
}

// Initialize create a gateway resource in specified region.
func (ag *ApiGateway) Initialize(region string, ctx context.Context) error {

	fmt.Println("initializing")

	cfg, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		panic(err)
	}
	cfg.Region = region
	client := apigateway.NewFromConfig(cfg)

	if ApiExistsInRegion(client, ag.Name, region) {
		return fmt.Errorf("an API already exists with name: %s in region %s", ag.Name, region)
	}

	// create new REST API
	newApi, err := client.CreateRestApi(ctx, &apigateway.CreateRestApiInput{
		Name: &ag.Name,
		EndpointConfiguration: &types.EndpointConfiguration{
			Types: []types.EndpointType{
				types.EndpointTypeRegional,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("cannot create new API: %w", err)
	}

	allowedHttpMethod := "ANY"
	authorizationType := "NONE"
	params := make(map[string]bool)
	params["method.request.path.proxy"] = true                  // ensures the path portion of the incoming request URL gets forwarded to the target site
	params["method.request.header.X-Forwarded-For-Temp"] = true // preserve X-Forwarded-For header by using a temp header X-My-X-Forwarded-For

	// allow all methods to new resource
	_, err = client.PutMethod(ctx, &apigateway.PutMethodInput{
		RestApiId:         newApi.Id,
		ResourceId:        newApi.RootResourceId,
		HttpMethod:        &allowedHttpMethod,
		AuthorizationType: &authorizationType,
		RequestParameters: params,
	})
	if err != nil {
		return fmt.Errorf("cannot create method: %w", err)
	}

	// make new resource route traffic to new host
	integrationParams := make(map[string]string)
	integrationParams["integration.request.path.proxy"] = "method.request.path.proxy"
	integrationParams["integration.request.header.X-Forwarded-For"] = "method.request.header.X-Forwarded-For-Temp"
	_, err = client.PutIntegration(ctx, &apigateway.PutIntegrationInput{
		RestApiId:             newApi.Id,
		ResourceId:            newApi.RootResourceId,
		Type:                  types.IntegrationTypeHttpProxy,
		HttpMethod:            &allowedHttpMethod,
		IntegrationHttpMethod: &allowedHttpMethod,
		Uri:                   &ag.Site,
		ConnectionType:        types.ConnectionTypeInternet,
		RequestParameters:     integrationParams,
	})
	if err != nil {
		return fmt.Errorf("cannot create integration: %w", err)
	}

	wildcardPath := "{proxy+}"
	wildcardHandler, err := client.CreateResource(ctx, &apigateway.CreateResourceInput{
		RestApiId: newApi.Id,
		ParentId:  newApi.RootResourceId,
		PathPart:  &wildcardPath,
	})
	if err != nil {
		return fmt.Errorf("cannot create wildcard handler: %w", err)
	}

	// handle requests received for the wildcard handler
	_, err = client.PutMethod(ctx, &apigateway.PutMethodInput{
		RestApiId:         newApi.Id,
		ResourceId:        wildcardHandler.Id,
		HttpMethod:        &allowedHttpMethod,
		AuthorizationType: &authorizationType,
		RequestParameters: params,
	})
	if err != nil {
		return fmt.Errorf("cannot create wildcard method input: %w", err)
	}

	_, err = client.PutIntegration(ctx, &apigateway.PutIntegrationInput{
		RestApiId:             newApi.Id,
		ResourceId:            wildcardHandler.Id,
		Type:                  types.IntegrationTypeHttpProxy,
		HttpMethod:            &allowedHttpMethod,
		IntegrationHttpMethod: &allowedHttpMethod,
		Uri:                   &ag.Site,
		ConnectionType:        types.ConnectionTypeInternet,
		RequestParameters:     integrationParams,
	})
	if err != nil {
		return fmt.Errorf("cannot integrate wildcard method: %w", err)
	}

	// create deployment resource so the new API is callable
	stageName := "ProxyStage"
	_, err = client.CreateDeployment(context.TODO(), &apigateway.CreateDeploymentInput{
		RestApiId: newApi.Id,
		StageName: &stageName,
	})
	if err != nil {
		return err
	}

	ag.Endpoints = append(ag.Endpoints, fmt.Sprintf("%s.execute-api.%s.amazonaws.com", *newApi.Id, region))

	return nil
}

// Reroute sends the original request through a proxy
func (ag *ApiGateway) Reroute(request *http.Request) *http.Request {
	// use a random endpoints as proxy

	fmt.Printf("before modification: %+v\n", request.Header)

	endpoint := ag.Endpoints[rand.Intn(len(ag.Endpoints)-1)]

	//fmt.Printf("request uri: %s\n", request.URL.)

	proxyUrl, err := url.Parse("https://" + endpoint + "/ProxyStage/" + request.Host)
	if err != nil {
		fmt.Printf("Error parsing url: %s", err)
		return request
	}
	request.URL = proxyUrl
	request.Host = endpoint

	// generate X-Forwarded-For header if original request does not have it
	// and move original X-Forwarded-For to a temp header
	val := request.Header.Get("X-Forwarded-For")
	if val == "" {
		randIp := randomIpv4().String()
		request.Header.Add("X-Forwarded-For-Temp", randIp)
	} else {
		request.Header.Add("X-Forwarded-For-Temp", val)
	}
	request.Header.Del("X-Forwarded-For")

	fmt.Printf("after modification: %+v\n", request.Header)

	return request
}

func (ag *ApiGateway) GetGateways(region string, ctx context.Context) (*[]types.RestApi, error) {
	var result []types.RestApi
	defaultPosition := ""
	var defaultLimit int32 = 500
	complete := false

	cfg, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		panic(err)
	}
	cfg.Region = region
	client := apigateway.NewFromConfig(cfg)

	for !complete {
		inputParams := apigateway.GetRestApisInput{
			Limit: &defaultLimit,
		}
		if defaultPosition != "" {
			inputParams.Position = &defaultPosition
		}
		response, err := client.GetRestApis(ctx, &inputParams)
		if err != nil {
			return &result, fmt.Errorf("cannot get rest apis: %w", err)
		}

		if response != nil && response.Position != nil {
			result = append(result, response.Items...)
			defaultPosition = *response.Position
		} else {
			// no pagination or end of pagination
			result = append(result, response.Items...)
			complete = true
		}
	}

	return &result, nil
}

func (ag *ApiGateway) GetEndpoints(region string, ctx context.Context) (*[]string, error) {
	apis, err := ag.GetGateways(region, ctx)
	if err != nil {
		return &[]string{}, err
	}

	var endpoints []string
	for _, i := range *apis {
		endpoints = append(endpoints, fmt.Sprintf("%s.execute-api.%s.amazonaws.com", *i.Id, region))
	}

	return &endpoints, nil
}

func (ag *ApiGateway) DeleteGateways(region string, ctx context.Context) (*[]string, error) {

	cfg, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		panic(err)
	}
	cfg.Region = region
	client := apigateway.NewFromConfig(cfg)

	var deletedIds []string
	apis, err := ag.GetGateways(region, ctx)
	if err != nil {
		return &deletedIds, err
	}
	for _, api := range *apis {
		if _, err := client.DeleteRestApi(ctx, &apigateway.DeleteRestApiInput{
			RestApiId: api.Id,
		}); err != nil {
			return &deletedIds, err
		}
		deletedIds = append(deletedIds, *api.Id)

	}

	return &deletedIds, nil
}
