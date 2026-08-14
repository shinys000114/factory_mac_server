CC ?= cc
AR ?= ar
CFLAGS ?= -O2 -g
CFLAGS += -Wall -Wextra -Werror -fPIC -Iinclude $(shell pkg-config --cflags libcurl)
LDLIBS += $(shell pkg-config --libs libcurl)

LIB_STATIC := libmacclient.a
LIB_SHARED := libmacclient.so
LIB_OBJECT := src/mac_client.o
UTILITY := macctl

.PHONY: all library example clean

all: library example

library: $(LIB_STATIC) $(LIB_SHARED)

example: $(UTILITY)

$(LIB_STATIC): $(LIB_OBJECT)
	$(AR) rcs $@ $^

$(LIB_SHARED): $(LIB_OBJECT)
	$(CC) -shared -o $@ $^ $(LDLIBS)

$(UTILITY): examples/macctl.o $(LIB_STATIC)
	$(CC) -o $@ examples/macctl.o $(LIB_STATIC) $(LDLIBS)

src/%.o: src/%.c include/mac_client.h
	$(CC) $(CFLAGS) -c -o $@ $<

examples/%.o: examples/%.c include/mac_client.h
	$(CC) $(CFLAGS) -c -o $@ $<

clean:
	$(RM) $(LIB_OBJECT) examples/macctl.o $(LIB_STATIC) $(LIB_SHARED) $(UTILITY)
