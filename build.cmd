@echo off
setlocal
pushd "%~dp0" || exit /b 1
set "GOTOOLCHAIN=local"
set "CGO_ENABLED=0"
set "GOOS=windows"
set "GOARCH=amd64"
set "GOCACHE=%CD%\.tools\build-cache"
set "GOPATH=%CD%\.tools\gopath"
set "PORTAL_GO=go"
if exist ".tools\go\bin\go.exe" set "PORTAL_GO=%CD%\.tools\go\bin\go.exe"
"%PORTAL_GO%" test -timeout 90s ./... || goto failed
"%PORTAL_GO%" vet ./... || goto failed
"%PORTAL_GO%" build -trimpath -o portal.exe . || goto failed
popd
exit /b 0
:failed
popd
exit /b 1
