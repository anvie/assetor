APP_NAME = assetor

# Define the current platform and the target Linux platform
CURRENT_PLATFORM = $(shell go env GOOS)
CURRENT_ARCH = $(shell go env GOARCH)
TARGET_LINUX_PLATFORM = linux
TARGET_LINUX_ARCH = amd64
OUTDIR=bin

all: $(OUTDIR)/$(APP_NAME) $(OUTDIR)/$(APP_NAME)_linux

$(OUTDIR)/$(APP_NAME): ./*.go
	@echo "Building for $(CURRENT_PLATFORM)/$(CURRENT_ARCH)..."
	go build -o $@ .

# Build command for Linux platform
$(OUTDIR)/$(APP_NAME)_linux: ./*.go
	@echo "Building for $(TARGET_LINUX_PLATFORM)/$(TARGET_LINUX_ARCH)..."
	GOOS=$(TARGET_LINUX_PLATFORM) GOARCH=$(TARGET_LINUX_ARCH) go build -o $@ .

clean:
	@echo "Cleaning..."
	rm -f $(OUTDIR)/$(APP_NAME) $(OUTDIR)/$(APP_NAME)_linux



