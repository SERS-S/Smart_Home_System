#ifndef MQTT_MANAGER_H
#define MQTT_MANAGER_H

#include <stdbool.h>
#include "esp_err.h"

esp_err_t mqtt_manager_init(void);
void mqtt_manager_wait_connected(void);

int mqtt_manager_publish(
    const char *topic,
    const char *data,
    int qos,
    int retain
);

bool mqtt_manager_is_connected(void);

#endif