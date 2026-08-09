//go:build darwin && cgo

package memoryguard

/*
#include <mach/mach.h>
#include <stdint.h>

static kern_return_t gopherink_available_memory(uint64_t *available) {
	mach_port_t host = mach_host_self();
	vm_size_t page_size = 0;
	vm_statistics64_data_t stats;
	mach_msg_type_number_t count = HOST_VM_INFO64_COUNT;
	kern_return_t result = host_page_size(host, &page_size);
	if (result == KERN_SUCCESS) {
		result = host_statistics64(host, HOST_VM_INFO64, (host_info64_t)&stats, &count);
	}
	if (result == KERN_SUCCESS) {
		*available = ((uint64_t)stats.free_count + (uint64_t)stats.inactive_count + (uint64_t)stats.speculative_count) * (uint64_t)page_size;
	}
	mach_port_deallocate(mach_task_self(), host);
	return result;
}
*/
import "C"

import "fmt"

func Available() (uint64, error) {
	var available C.uint64_t
	if result := C.gopherink_available_memory(&available); result != C.KERN_SUCCESS {
		return 0, fmt.Errorf("host_statistics64 failed: %d", int(result))
	}
	return uint64(available), nil
}
