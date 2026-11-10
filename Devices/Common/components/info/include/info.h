#ifndef INFO_H
#define INFO_H

void voltage_init(void);
void publish_info(
    char *device_id,
    char *version,
    char *topic
);

#endif