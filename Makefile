.PHONY: all client client-clean clean

# The default target builds the C client library and the macctl example.
all: client

client:
	$(MAKE) -C client all

client-clean:
	$(MAKE) -C client clean

clean: client-clean
