/*
 * Copyright (C) 2023 Nethesis S.r.l.
 * http://www.nethesis.it - info@nethesis.it
 *
 * SPDX-License-Identifier: GPL-2.0-only
 *
 * author: Edoardo Spadoni <edoardo.spadoni@nethesis.it>
 */

package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"io/ioutil"
	"strings"
	"time"

	"github.com/fatih/structs"
	"github.com/gin-gonic/gin"
	"github.com/nqd/flat"

	jwt "github.com/appleboy/gin-jwt/v2"

	"github.com/NethServer/ns-api-server/configuration"
	"github.com/NethServer/ns-api-server/logs"
	"github.com/NethServer/ns-api-server/methods"
	"github.com/NethServer/ns-api-server/models"
	"github.com/NethServer/ns-api-server/response"
)

type login struct {
	Username string `form:"username" json:"username" binding:"required"`
	Password string `form:"password" json:"password" binding:"required"`
}

var jwtMiddleware *jwt.GinJWTMiddleware
var identityKey = "id"

func InstanceJWT() *jwt.GinJWTMiddleware {
	if jwtMiddleware == nil {
		jwtMiddleware := InitJWT()
		return jwtMiddleware
	}
	return jwtMiddleware
}

func InitJWT() *jwt.GinJWTMiddleware {
	// define jwt middleware
	authMiddleware, errDefine := jwt.New(&jwt.GinJWTMiddleware{
		Realm:       "nethserver",
		Key:         []byte(configuration.Config.SecretJWT),
		Timeout:     time.Hour * 24, // 1 day
		MaxRefresh:  time.Hour * 24, // 1 day
		IdentityKey: identityKey,
		Authenticator: func(c *gin.Context) (interface{}, error) {
			// check login credentials exists
			var loginVals login
			if err := c.ShouldBind(&loginVals); err != nil {
				return "", jwt.ErrMissingLoginValues
			}

			// set login credentials
			username := loginVals.Username
			password := loginVals.Password

			// check login
			err := methods.CheckAuthentication(username, password)
			if err != nil {
				// login fail action
				logs.Logs.Info("[INFO][AUTH] authentication failed for user " + username + ": " + err.Error())

				// return JWT error
				return nil, jwt.ErrFailedAuthentication
			}

			// login ok action
			logs.Logs.Info("[INFO][AUTH] authentication success for user " + username)

			// return user auth model
			return &models.UserAuthorizations{
				Username: username,
			}, nil

		},
		PayloadFunc: func(data interface{}) jwt.MapClaims {
			// read current user
			if user, ok := data.(*models.UserAuthorizations); ok {
				// check if user require 2fa
				var required = methods.GetUserSecret(user.Username) != ""

				// create claims map
				return jwt.MapClaims{
					identityKey: user.Username,
					"role":      "",
					"actions":   []string{},
					"2fa":       required,
				}
			}

			// return claims map
			return jwt.MapClaims{}
		},
		IdentityHandler: func(c *gin.Context) interface{} {
			// handle identity and extract claims
			claims := jwt.ExtractClaims(c)

			// create user object
			user := &models.UserAuthorizations{
				Username: claims[identityKey].(string),
				Role:     "admin",
				Actions:  nil,
			}

			// return user
			return user
		},
		Authorizator: func(data interface{}, c *gin.Context) bool {
			// check token validation
			claims, _ := InstanceJWT().GetClaimsFromJWT(c)
			token, _ := InstanceJWT().ParseToken(c)

			// log request and body
			reqMethod := c.Request.Method
			reqURI := c.Request.RequestURI

			// check if token exists
			if !methods.CheckTokenValidation(claims["id"].(string), token.Raw) {
				// write logs
				logs.Logs.Info("[INFO][AUTH] authorization failed for user " + claims["id"].(string) + ". request " + reqMethod + " on " + reqURI)

				// not authorized
				return false
			}

			// extract body
			reqBody := ""
			if reqMethod == "POST" || reqMethod == "PUT" {
				// extract body
				var buf bytes.Buffer
				tee := io.TeeReader(c.Request.Body, &buf)
				body, _ := ioutil.ReadAll(tee)
				c.Request.Body = ioutil.NopCloser(&buf)

				// convert to map and flat it
				var jsonDyn map[string]interface{}
				json.Unmarshal(body, &jsonDyn)
				in, _ := flat.Flatten(jsonDyn, nil)

				// search for sensitve data, in sensitive list
				for k, _ := range in {
					for _, s := range configuration.Config.SensitiveList {
						if strings.Contains(strings.ToLower(k), strings.ToLower(s)) {
							in[k] = "XXX"
						}
					}
				}

				// unflat the map
				out, _ := flat.Unflatten(in, nil)

				// convert to json string
				jsonOut, _ := json.Marshal(out)

				// compose string
				reqBody = string(jsonOut)
			}

			logs.Logs.Info("[INFO][AUTH] authorization success for user " + claims["id"].(string) + ". request " + reqMethod + " on " + reqURI + " " + reqBody)

			// authorized
			return true
		},
		LoginResponse: func(c *gin.Context, code int, token string, t time.Time) {
			//get claims
			tokenObj, _ := InstanceJWT().ParseTokenString(token)
			claims := jwt.ExtractClaimsFromToken(tokenObj)

			// set token to valid, if not 2FA
			if !claims["2fa"].(bool) {
				methods.SetTokenValidation(claims["id"].(string), token)
			}

			// write logs
			logs.Logs.Info("[INFO][AUTH] login response success for user " + claims["id"].(string))

			// return 200 OK
			c.JSON(200, gin.H{"code": 200, "expire": t, "token": token})
		},
		LogoutResponse: func(c *gin.Context, code int) {
			//get claims
			tokenObj, _ := InstanceJWT().ParseToken(c)
			claims := jwt.ExtractClaimsFromToken(tokenObj)

			// set token to invalid
			methods.DelTokenValidation(claims["id"].(string), tokenObj.Raw)

			// write logs
			logs.Logs.Info("[INFO][AUTH] logout response success for user " + claims["id"].(string))

			// reutrn 200 OK
			c.JSON(200, gin.H{"code": 200})
		},
		Unauthorized: func(c *gin.Context, code int, message string) {
			// write logs
			logs.Logs.Info("[INFO][AUTH] unauthorized request: " + message)

			// response not authorized
			c.JSON(code, structs.Map(response.StatusUnauthorized{
				Code:    code,
				Message: message,
				Data:    nil,
			}))
			return
		},
		TokenLookup:   "header: Authorization, token: jwt",
		TokenHeadName: "Bearer",
		TimeFunc:      time.Now,
	})

	// check middleware errors
	if errDefine != nil {
		logs.Logs.Err("[ERR][AUTH] middleware definition error: " + errDefine.Error())
	}

	// init middleware
	errInit := authMiddleware.MiddlewareInit()

	// check error on initialization
	if errInit != nil {
		logs.Logs.Err("[ERR][AUTH] middleware initialization error: " + errInit.Error())
	}

	// return object
	return authMiddleware
}
