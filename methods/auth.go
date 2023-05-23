/*
 * Copyright (C) 2023 Nethesis S.r.l.
 * http://www.nethesis.it - info@nethesis.it
 *
 * This file is part of NethServer project.
 *
 * NethServer is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as published by
 * the Free Software Foundation, either version 3 of the License,
 * or any later version.
 *
 * NethServer is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with NethServer.  If not, see COPYING.
 *
 * author: Edoardo Spadoni <edoardo.spadoni@nethesis.it>
 */

package methods

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"strings"

	jwt "github.com/appleboy/gin-jwt/v2"
	"github.com/dgryski/dgoogauth"
	"github.com/fatih/structs"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	jwtl "github.com/golang-jwt/jwt"
	"github.com/pkg/errors"

	"github.com/NethServer/ns-api-server/configuration"
	"github.com/NethServer/ns-api-server/models"
	"github.com/NethServer/ns-api-server/response"
	"github.com/NethServer/ns-api-server/utils"
)

var ctx = context.Background()

func CheckAuthentication(username string, password string) error {
	// execute login command

	return nil
}

func OTPVerify(c *gin.Context) {
	// get payload
	var jsonOTP models.OTPJson
	if err := c.ShouldBindBodyWith(&jsonOTP, binding.JSON); err != nil {
		c.JSON(http.StatusBadRequest, structs.Map(response.StatusBadRequest{
			Code:    400,
			Message: "request fields malformed",
			Data:    err.Error(),
		}))
		return
	}

	// verify JWT
	if !ValidateAuth(jsonOTP.Token, false) {
		c.JSON(http.StatusBadRequest, structs.Map(response.StatusBadRequest{
			Code:    400,
			Message: "JWT token invalid",
			Data:    "",
		}))
		return
	}

	// get secret for the user
	secret := GetUserSecret(jsonOTP.Username)

	// check secret
	if len(secret) == 0 {
		c.JSON(http.StatusNotFound, structs.Map(response.StatusNotFound{
			Code:    404,
			Message: "User secret not found",
			Data:    "",
		}))
		return
	}

	// set OTP configuration
	otpc := &dgoogauth.OTPConfig{
		Secret:      secret,
		WindowSize:  3,
		HotpCounter: 0,
	}

	// verifiy OTP
	result, err := otpc.Authenticate(jsonOTP.OTP)
	if err != nil || !result {
		c.JSON(http.StatusBadRequest, structs.Map(response.StatusBadRequest{
			Code:    400,
			Message: "OTP token invalid",
			Data:    "",
		}))
		return
	}

	// check if 2FA was disabled
	status, err := os.ReadFile(configuration.Config.SecretsDir + "/" + jsonOTP.Username + "/status")
	statusOld := strings.TrimSpace(string(status[:]))

	// then clean all previous tokens
	if statusOld == "0" || statusOld == "" {
		// open file
		f, _ := os.OpenFile(configuration.Config.TokensDir+"/"+jsonOTP.Username, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
		defer f.Close()

		// write file with tokens
		_, err := f.WriteString("")

		// check error
		if err != nil {
			c.JSON(http.StatusBadRequest, structs.Map(response.StatusBadRequest{
				Code:    400,
				Message: "Clean previous tokens error",
				Data:    err,
			}))
			return
		}
	}

	// set auth token to valid
	if !SetTokenValidation(jsonOTP.Username, jsonOTP.Token) {
		c.JSON(http.StatusBadRequest, structs.Map(response.StatusBadRequest{
			Code:    400,
			Message: "Token validation set error",
			Data:    "",
		}))
		return
	}

	// set 2FA to enabled
	f, _ := os.OpenFile(configuration.Config.SecretsDir+"/"+jsonOTP.Username+"/status", os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	defer f.Close()

	// write file with 2fa status
	_, err = f.WriteString("1")

	// check error
	if err != nil {
		c.JSON(http.StatusBadRequest, structs.Map(response.StatusBadRequest{
			Code:    400,
			Message: "Status set error",
			Data:    err,
		}))
		return
	}

	// response
	c.JSON(http.StatusOK, structs.Map(response.StatusOK{
		Code:    200,
		Message: "OTP verified",
		Data:    jsonOTP.Token,
	}))
}

func QRCode(c *gin.Context) {
	// generate random secret
	secret := make([]byte, 20)
	_, err := rand.Read(secret)
	if err != nil {
		utils.LogError(errors.Wrap(err, "[2FA] Failed to generate random secret for QRCode"))
	}

	// convert to string
	secretBase32 := base32.StdEncoding.EncodeToString(secret)

	// get claims from token
	claims := jwt.ExtractClaims(c)

	// define issuer
	account := claims["id"].(string)
	issuer := configuration.Config.Issuer2FA

	// set secret for user
	result, setSecret := SetUserSecret(account, secretBase32)
	if !result {
		c.JSON(http.StatusBadRequest, structs.Map(response.StatusBadRequest{
			Code:    400,
			Message: "User secret set error",
			Data:    "",
		}))
		return
	}

	// define URL
	URL, err := url.Parse("otpauth://totp")
	if err != nil {
		utils.LogError(errors.Wrap(err, "[2FA] Failed to parse URL for QRCode"))
	}

	// add params
	URL.Path += "/" + issuer + ":" + account
	params := url.Values{}
	params.Add("secret", setSecret)
	params.Add("issuer", issuer)
	params.Add("algorithm", "SHA1")
	params.Add("digits", "6")
	params.Add("period", "30")

	// print url
	URL.RawQuery = params.Encode()

	// response
	c.JSON(http.StatusOK, structs.Map(response.StatusOK{
		Code:    200,
		Message: "QR code string",
		Data:    gin.H{"url": URL.String(), "key": setSecret},
	}))
}

func Get2FAStatus(c *gin.Context) {
	// get claims from token
	claims := jwt.ExtractClaims(c)

	// get status
	status, err := os.ReadFile(configuration.Config.SecretsDir + "/" + claims["id"].(string) + "/status")
	statusS := strings.TrimSpace(string(status[:]))

	// handle response
	var message = "2FA set for this user"
	if !(statusS == "1") || err != nil {
		message = "2FA not set for this user"
		statusS = "0"
	}

	// return response
	c.JSON(http.StatusOK, structs.Map(response.StatusOK{
		Code:    200,
		Message: message,
		Data:    statusS == "1",
	}))
}

func Del2FAStatus(c *gin.Context) {
	// get claims from token
	claims := jwt.ExtractClaims(c)

	// revocate secret
	errRevocate := os.Remove(configuration.Config.SecretsDir + "/" + claims["id"].(string) + "/secret")
	if errRevocate != nil {
		c.JSON(http.StatusBadRequest, structs.Map(response.StatusBadRequest{
			Code:    403,
			Message: "Error in revocate 2FA for user",
			Data:    nil,
		}))
		return
	}

	// set 2FA to disabled
	f, _ := os.OpenFile(configuration.Config.SecretsDir+"/"+claims["id"].(string)+"/status", os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	defer f.Close()

	// write file with tokens
	_, err := f.WriteString("0")

	// check error
	if err != nil {
		c.JSON(http.StatusBadRequest, structs.Map(response.StatusBadRequest{
			Code:    400,
			Message: "2FA not revocated",
			Data:    "",
		}))
		return
	}

	// response
	c.JSON(http.StatusOK, structs.Map(response.StatusOK{
		Code:    200,
		Message: "2FA revocate successfully",
		Data:    "",
	}))
}

func GetUserSecret(username string) string {
	// get secret
	secret, err := os.ReadFile(configuration.Config.SecretsDir + "/" + username + "/secret")

	// handle error
	if err != nil {
		return ""
	}

	// return string
	return string(secret[:])
}

func SetUserSecret(username string, secret string) (bool, string) {
	// get secret
	secretB, _ := os.ReadFile(configuration.Config.SecretsDir + "/" + username + "/secret")

	// check error
	if len(string(secretB[:])) == 0 {
		// check if dir exists, otherwise create it
		if _, errD := os.Stat(configuration.Config.SecretsDir + "/" + username); os.IsNotExist(errD) {
			_ = os.MkdirAll(configuration.Config.SecretsDir+"/"+username, 0700)
		}

		// open file
		f, _ := os.OpenFile(configuration.Config.SecretsDir+"/"+username+"/secret", os.O_WRONLY|os.O_CREATE, 0600)
		defer f.Close()

		// write file with secret
		_, err := f.WriteString(secret)

		// check error
		if err != nil {
			return false, ""
		}

		return true, secret
	}

	return true, string(secretB[:])
}

func CheckTokenValidation(username string, token string) bool {
	// read whole file
	secrestListB, err := ioutil.ReadFile(configuration.Config.TokensDir + "/" + username)
	if err != nil {
		return false
	}
	secrestList := string(secrestListB)

	// //check whether s contains substring text
	return strings.Contains(secrestList, token)
}

func SetTokenValidation(username string, token string) bool {
	// open file
	f, _ := os.OpenFile(configuration.Config.TokensDir+"/"+username, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0600)
	defer f.Close()

	// write file with tokens
	_, err := f.WriteString(token + "\n")

	// check error
	if err != nil {
		return false
	}

	return true
}

func RemoveTokenValidation(username string, token string) bool {
	// read whole file
	secrestListB, errR := ioutil.ReadFile(configuration.Config.TokensDir + "/" + username)
	if errR != nil {
		return false
	}
	secrestList := string(secrestListB)

	// match token to remove
	res := strings.Replace(secrestList, token, "", 1)

	// open file
	f, _ := os.OpenFile(configuration.Config.TokensDir+"/"+username, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	defer f.Close()

	// write file with tokens
	_, err := f.WriteString(strings.TrimSpace(res) + "\n")

	// check error
	if err != nil {
		return false
	}

	return true

}

func ValidateAuth(tokenString string, ensureTokenExists bool) bool {
	// convert token string and validate it
	if tokenString != "" {
		token, err := jwtl.Parse(tokenString, func(token *jwtl.Token) (interface{}, error) {
			// validate the alg
			if _, ok := token.Method.(*jwtl.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("Unexpected signing method: %v", token.Header["alg"])
			}

			// return secret
			return []byte(configuration.Config.SecretJWT), nil
		})

		if err != nil {
			utils.LogError(errors.Wrap(err, "[JWT] error in JWT token validation"))
			return false
		}

		if claims, ok := token.Claims.(jwtl.MapClaims); ok && token.Valid {
			if claims["id"] != nil {
				if ensureTokenExists {
					username := claims["id"].(string)

					if !CheckTokenValidation(username, tokenString) {
						utils.LogError(errors.New("[JWT] error JWT token not found"))
						return false
					}
				}
				return true
			}
		} else {
			utils.LogError(errors.Wrap(err, "[JWT] error in JWT token claims"))
			return false
		}
	}
	return false
}