#include <string>
#include <fstream>
#include <sstream>

#include "spdlog/spdlog.h"

extern "C" int mosquitto_broker_main(int argc, char *argv[]);

int main(int argc, char *argv[])
{
    std::string listener_address = MOSQUITTO_HOST;
    unsigned int listener_port = MOSQUITTO_PORT;
    bool listener_anonymous_allow = MOSQUITTO_ANONYMOUS_ALLOW;

    std::stringstream ss;
    ss << "listener" << " " << listener_port
       << " " << listener_address << "\n\n"
       << " " << "listener_allow_anonymous" << " " << (listener_anonymous_allow ? "true" : "false") << "\n\n"
       << "log_dest" << " " << "stdout" << "\n\n"
       << "log_type" << " " << "all" << "\n\n";

    std::ofstream file("/etc/mosquitto/mosquitto.conf");
    if (!file.is_open())
    {
        spdlog::error("Failed to open Mosquitto configuration file.");
        return 1;
    }

    file << ss.str();
    file.close();

    spdlog::info("Mosquitto configuration file created successfully.");

    spdlog::info("Starting Mosquitto broker...");
    return mosquitto_broker_main(argc, argv);
}
